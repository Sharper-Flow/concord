#!/usr/bin/env bash
# Disposable GitHub Ubuntu runner only. Never source this file or run as root.
set -Eeuo pipefail
umask 077
trap 'printf "native provisioning failed at line %s\n" "$LINENO" >&2' ERR

die() { printf '%s\n' "$*" >&2; exit 1; }
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted &&
   ${CONCORD_DISPOSABLE_CI_PROVISION:-} == 1 && ${ImageOS:-} == ubuntu24 ]] ||
    die 'provisioning is restricted to explicitly authorized disposable GitHub Ubuntu 24.04 runners'
[[ $EUID != 0 && $(uname -m) == x86_64 ]] || die 'unprivileged amd64 build required'
# shellcheck source=/dev/null
source /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 24.04 ]] || die 'Ubuntu 24.04 required'
[[ -n ${RUNNER_TEMP:-} && -d $RUNNER_TEMP && -n ${GITHUB_WORKSPACE:-} ]] || die 'runner paths missing'
[[ $# == 1 && $1 == --upstream-compatibility ]] || die 'unsupported provisioning arguments'
helper="$GITHUB_WORKSPACE/scripts/ci-native-provision.py"
evidence="$RUNNER_TEMP/upstream-compatibility-evidence"
mkdir -m 700 "$evidence"
exec > >(tee "$evidence/provision.log") 2>&1
printf 'ImageOS=%s ImageVersion=%s\n' "$ImageOS" "${ImageVersion:-unknown}"
uname -a
dpkg --print-architecture
[[ $(dpkg --print-architecture) == amd64 ]] || die 'amd64 dpkg required'
work=$(mktemp -d "$RUNNER_TEMP/native-isolation-build.XXXXXXXX")
compat_root=''
cleanup() {
    local status=$?
    trap - EXIT
    local cleanup_status=0
    if [[ -n $compat_root ]]; then
        if sudo test -f "$compat_root/cleanup-safe"; then
            sudo rm -rf --one-file-system -- "$compat_root" || cleanup_status=1
        else
            printf "root cleanup safety not proved; retaining %s until VM disposal\n" "$compat_root" >&2
            cleanup_status=1
        fi
    else
        rm -rf -- "$work" || cleanup_status=1
    fi
    printf "cleanup_status=%s\n" "$cleanup_status"
    if ((cleanup_status)); then status=1; fi
    exit "$status"
}
trap cleanup EXIT
rm -rf -- "$work"
compat_root=$(sudo mktemp -d /var/lib/concord-upstream.XXXXXXXX)
sudo chmod 0755 "$compat_root"
sudo install -o root -g root -m 0600 "$helper" "$compat_root/helper.py"
sudo touch "$compat_root/cleanup-safe"
sudo mkdir -m 0700 "$compat_root/work"
sudo chown "$(id -u):$(id -g)" "$compat_root/work"
work="$compat_root/work"
mkdir "$work/debs" "$evidence/package-controls" "$evidence/package-files"
dpkg --audit > "$evidence/dpkg-audit-before.txt"
[[ ! -s $evidence/dpkg-audit-before.txt ]] || die 'runner has an incomplete package transaction'

# AppArmor's postinst reloads all profiles. Never install or upgrade that package.
auth=(-o APT::Get::AllowUnauthenticated=false -o Acquire::AllowInsecureRepositories=false -o Acquire::AllowDowngradeToInsecureRepositories=false)
sudo apt-get "${auth[@]}" update
apt-get indextargets > "$evidence/apt-index-targets.txt"
apt_indices=(/var/lib/apt/lists/*_InRelease /var/lib/apt/lists/*_Packages*)
python3 "$helper" fingerprint-files "${apt_indices[@]}" /usr/share/keyrings/ubuntu-archive-keyring.gpg > "$evidence/apt-index-identities.json"
python3 "$helper" pins > "$work/pins.txt"
mapfile -t pins < "$work/pins.txt"
sudo apt-get -s --no-install-recommends --no-remove install "${pins[@]}" | tee "$evidence/apt-plan.txt" > /dev/null
python3 "$helper" transaction "$evidence/apt-plan.txt" "$evidence/installed-before.json" > "$work/transaction.txt"
mapfile -t changes < "$work/transaction.txt"
if ((${#changes[@]})); then
  (
    cd "$work/debs"
    apt-get "${auth[@]}" download "${changes[@]}"
  )
fi
debs=("$work/debs/"*.deb)
install_debs=()
for deb in "${debs[@]}"; do
    [[ -f $deb ]] || continue
    package=$(dpkg-deb -f "$deb" Package)
    sha256sum "$deb" >> "$evidence/package-archives.sha256"
    dpkg-deb -c "$deb" > "$evidence/package-files/$package.txt"
    dpkg-deb -e "$deb" "$evidence/package-controls/$package"
    python3 "$helper" audit-control "$evidence/package-controls/$package"
    mkdir "$evidence/package-controls/old-$package"
    for phase in preinst postinst prerm postrm triggers; do
        old_control="/var/lib/dpkg/info/$package.$phase"
        if [[ ! -f $old_control ]]; then old_control="/var/lib/dpkg/info/$package:amd64.$phase"; fi
        if [[ -f $old_control ]]; then
            cp "$old_control" "$evidence/package-controls/old-$package/$phase"
        fi
    done
    python3 "$helper" audit-control "$evidence/package-controls/old-$package"
    install_debs+=("$deb")
done
[[ ${#install_debs[@]} == ${#changes[@]} ]] || die 'downloaded package count differs from transaction'
if ((${#install_debs[@]})); then
    sudo apt-get -s --no-install-recommends --no-remove install "${install_debs[@]}" | tee "$evidence/apt-local-plan.txt" > /dev/null
    python3 "$helper" transaction "$evidence/apt-local-plan.txt" "$work/installed-local.json" > "$work/local-transaction.txt"
    cmp "$work/transaction.txt" "$work/local-transaction.txt"
    # A local-deb argument need not be APT's selected archive source.
    # Offline acquisition uses Dir::Cache::archives, not arbitrary argument paths.
    # Point it at the already downloaded/audited bytes, including epoch filenames.
    sha256sum --check --strict "$evidence/package-archives.sha256"
    sudo env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get -o "Dir::Cache::archives=$work/debs" -y --no-download --no-install-recommends --no-remove install "${install_debs[@]}"
fi
python3 "$helper" verify-pins
dpkg --audit > "$evidence/dpkg-audit-after.txt"
[[ ! -s $evidence/dpkg-audit-after.txt ]] || die 'provisioning left an incomplete package transaction'
dpkg-query -W > "$evidence/installed-after.txt"
dpkg-query -L "${pins[@]%%=*}" > "$evidence/pinned-package-files.txt"
python3 "$helper" fingerprint-files /usr/bin/gcc-13 /usr/bin/python3.12 /usr/bin/ninja \
    /usr/bin/pkg-config /usr/bin/meson > "$evidence/build-tools.json"
{
    /usr/bin/gcc-13 --version
    /usr/bin/python3.12 --version
    /usr/bin/ninja --version
    /usr/bin/pkg-config --version
    /usr/bin/python3.12 /usr/bin/meson --version
} > "$evidence/build-tool-versions.txt"
archive="$work/bubblewrap-0.12.0.tar.xz"
curl --fail --location --proto '=https' --tlsv1.2 --retry 3 \
    https://github.com/containers/bubblewrap/releases/download/v0.12.0/bubblewrap-0.12.0.tar.xz -o "$archive"
printf '%s  %s\n' 9760d007363e3abba7c747489910f9f82d9fca53ba3bd3282e396fa3c97a3314 "$archive" | sha256sum --check --strict
printf '%s\n' 2a76602a8c71f36c1527cf9fc3417d9149822e0c > "$evidence/upstream-commit.txt"
sha256sum "$archive" > "$evidence/source-archive.sha256"
tar --extract --xz --no-same-owner --no-same-permissions --file "$archive" --directory "$work"
source_dir="$work/bubblewrap-0.12.0"
python3 "$helper" repair-upstream-source "$archive" "$source_dir" > "$evidence/upstream-harness-repairs.json"
build="$work/build"
printf "[binaries]\nc = '/usr/bin/gcc-13'\nar = '/usr/bin/ar'\nstrip = '/usr/bin/strip'\npkgconfig = '/usr/bin/pkg-config'\n" > "$work/native.ini"
env -i HOME="$work" PATH=/usr/bin:/bin LANG=C.UTF-8 NINJA=/usr/bin/ninja \
    /usr/bin/python3.12 /usr/bin/meson setup "$build" "$source_dir" \
    --native-file "$work/native.ini" --buildtype=release --prefix=/usr --bindir=bin --wrap-mode=nodownload \
    -Dpython=/usr/bin/python3.12 -Dman=disabled -Dbash_completion=disabled -Dzsh_completion=disabled \
    -Dselinux=disabled -Dtests=true -Dbuild_rpath= -Dinstall_rpath=
env -i HOME="$work" PATH=/usr/bin:/bin LANG=C.UTF-8 /usr/bin/ninja -C "$build"
cp "$work/native.ini" "$evidence/"
cp "$build/meson-logs/meson-log.txt" "$evidence/"
cp "$build/meson-info/intro-buildoptions.json" "$evidence/"
readelf -d "$build/bwrap" > "$evidence/bwrap-dynamic.txt"
if grep -E '\((RPATH|RUNPATH)\)' "$evidence/bwrap-dynamic.txt"; then die 'RPATH is forbidden'; fi
# Ownership is hygiene, not a boundary against the runner's passwordless sudo.
# Never compile or invoke repository/candidate/native workloads as root.
sudo chown -hR root:root "$work"
sudo chmod -R go-w "$work"
sudo -n -u root -g root -- env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LANG=C.UTF-8 /usr/bin/python3.12 -I -B \
    "$compat_root/helper.py" upstream-compatibility "$work" | tee "$evidence/qualification.json" > /dev/null
python3 "$helper" qualify-compatibility-crashes "$evidence/qualification.json" > "$evidence/crash-metadata.json"
digest=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["bwrap_sha256"])' "$evidence/qualification.json")
[[ $digest =~ ^[0-9a-f]{64}$ ]] || die 'invalid compatibility output digest'
printf 'bwrap-sha256=%s\n' "$digest" >> "$GITHUB_OUTPUT"
exit 0
