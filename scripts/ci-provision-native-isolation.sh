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
helper="$GITHUB_WORKSPACE/scripts/ci-native-provision.py"
evidence="$RUNNER_TEMP/native-isolation-evidence"
mkdir -m 700 "$evidence"
exec > >(tee "$evidence/provision.log") 2>&1
printf 'ImageOS=%s ImageVersion=%s\n' "$ImageOS" "${ImageVersion:-unknown}"
uname -a
dpkg --print-architecture
[[ $(dpkg --print-architecture) == amd64 ]] || die 'amd64 dpkg required'
work=$(mktemp -d "$RUNNER_TEMP/native-isolation-build.XXXXXXXX")
trap 'rm -rf -- "$work"' EXIT
mkdir "$work/debs" "$evidence/package-controls" "$evidence/package-files"
dpkg --audit > "$evidence/dpkg-audit-before.txt"
[[ ! -s $evidence/dpkg-audit-before.txt ]] || die 'runner has an incomplete package transaction'

# AppArmor's postinst reloads all profiles. Refuse its installation/upgrade;
# extract the extra profile package without running any of its scripts instead.
sudo apt-get update
apt-get indextargets > "$evidence/apt-index-targets.txt"
apt_indices=(/var/lib/apt/lists/*_InRelease /var/lib/apt/lists/*_Packages*)
python3 "$helper" fingerprint-files "${apt_indices[@]}" /usr/share/keyrings/ubuntu-archive-keyring.gpg > "$evidence/apt-index-identities.json"
python3 "$helper" pins > "$work/pins.txt"
mapfile -t pins < "$work/pins.txt"
sudo apt-get -s --no-install-recommends --no-remove install "${pins[@]}" > "$evidence/apt-plan.txt"
python3 "$helper" transaction "$evidence/apt-plan.txt" "$evidence/installed-before.json" > "$work/transaction.txt"
mapfile -t changes < "$work/transaction.txt"
profile_pin=$(python3 "$helper" profile-pin)
(
    cd "$work/debs"
    apt-get download "$profile_pin" "${changes[@]}"
)
debs=("$work/debs/"*.deb)
[[ -f ${debs[0]} ]] || die 'no staged packages'
install_debs=()
for deb in "${debs[@]}"; do
    package=$(dpkg-deb -f "$deb" Package)
    version=$(dpkg-deb -f "$deb" Version)
    sha256sum "$deb" >> "$evidence/package-archives.sha256"
    dpkg-deb -c "$deb" > "$evidence/package-files/$package.txt"
    dpkg-deb -e "$deb" "$evidence/package-controls/$package"
    if [[ $package == apparmor-profiles ]]; then
        [[ $package=$version == "$profile_pin" ]] || die 'vendor profile package drift'
        mkdir "$work/profile-package"
        dpkg-deb -x "$deb" "$work/profile-package"
    else
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
    fi
done
if ((${#install_debs[@]})); then
    sudo apt-get -s --no-install-recommends --no-remove install "${install_debs[@]}" > "$evidence/apt-local-plan.txt"
    python3 "$helper" transaction "$evidence/apt-local-plan.txt" "$work/installed-local.json" > "$work/local-transaction.txt"
    cmp "$work/transaction.txt" "$work/local-transaction.txt"
    sudo env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get -y --no-download --no-install-recommends --no-remove install "${install_debs[@]}"
fi
python3 "$helper" verify-pins
dpkg --audit > "$evidence/dpkg-audit-after.txt"
[[ ! -s $evidence/dpkg-audit-after.txt ]] || die 'provisioning left an incomplete package transaction'
dpkg-query -W > "$evidence/installed-after.txt"
dpkg-query -L "${pins[@]%%=*}" > "$evidence/pinned-package-files.txt"
python3 "$helper" fingerprint-files /usr/bin/gcc-13 /usr/bin/python3.12 /usr/bin/ninja \
    /usr/bin/pkg-config /usr/bin/meson /usr/sbin/apparmor_parser > "$evidence/build-tools.json"
{
    /usr/bin/gcc-13 --version
    /usr/bin/python3.12 --version
    /usr/bin/ninja --version
    /usr/bin/pkg-config --version
    /usr/bin/python3.12 /usr/bin/meson --version
    /usr/sbin/apparmor_parser --version
} > "$evidence/build-tool-versions.txt"
# Keep the parser package's actual maintainer scripts as evidence even though
# they were not executed by this provisioner.
cp /var/lib/dpkg/info/apparmor.* "$evidence/package-controls/"

archive="$work/bubblewrap-0.12.0.tar.xz"
curl --fail --location --proto '=https' --tlsv1.2 --retry 3 \
    https://github.com/containers/bubblewrap/releases/download/v0.12.0/bubblewrap-0.12.0.tar.xz -o "$archive"
printf '%s  %s\n' 9760d007363e3abba7c747489910f9f82d9fca53ba3bd3282e396fa3c97a3314 "$archive" | sha256sum --check --strict
printf '%s\n' 2a76602a8c71f36c1527cf9fc3417d9149822e0c > "$evidence/upstream-commit.txt"
sha256sum "$archive" > "$evidence/source-archive.sha256"
tar --extract --xz --no-same-owner --no-same-permissions --file "$archive" --directory "$work"
source_dir="$work/bubblewrap-0.12.0"
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
sudo install -o root -g root -m 0755 "$build/bwrap" /usr/bin/bwrap
[[ $(stat -c '%u:%g:%a' /usr/bin/bwrap) == 0:0:755 && ! -L /usr/bin/bwrap ]] || die 'installed launcher ownership/mode mismatch'
cmp "$build/bwrap" /usr/bin/bwrap
[[ $(/usr/bin/bwrap --version) == 'bubblewrap 0.12.0' ]] || die 'launcher version mismatch'
sha256sum /usr/bin/bwrap > "$evidence/installed-bwrap.sha256"
ldd /usr/bin/bwrap > "$evidence/bwrap-ldd.txt"
python3 "$helper" fingerprint-runtime /usr/bin/bwrap > "$evidence/installed-bwrap-runtime.json"
node_path=$(command -v node)
bun_path=$(command -v bun)
[[ $("$node_path" --version) == v24.15.0 && $("$bun_path" --version) == 1.4.0 ]] || die 'runtime pin mismatch'
printf 'node=%s\nbun=%s\n' "$("$node_path" --version)" "$("$bun_path" --version)" > "$evidence/runtime-versions.txt"
python3 "$helper" fingerprint-files "$node_path" "$bun_path" > "$evidence/installed-node-bun.json"
stat /usr/bin/bwrap > "$evidence/installed-bwrap.stat"
getcap /usr/bin/bwrap > "$evidence/installed-bwrap.capabilities"
[[ ! -s $evidence/installed-bwrap.capabilities ]] || die 'file capabilities forbidden'

profile="$work/profile-package/usr/share/apparmor/extra-profiles/bwrap-userns-restrict"
[[ -f $profile && ! -L $profile ]] || die 'exact vendor profile missing'
[[ ! -e /etc/apparmor.d/local/bwrap && ! -e /etc/apparmor.d/local/bwrap-userns-restrict &&
   ! -e /etc/apparmor.d/local/unpriv_bwrap ]] || die 'local bwrap policy overrides are not admitted'
cp "$profile" "$evidence/vendor-profile"
sudo install -o root -g root -m 0644 "$profile" /etc/apparmor.d/bwrap-userns-restrict
# Preprocessing captures effective ABI/tunables/includes. Loading this one file
# never invokes a global profile reload or changes a kernel restriction.
sudo /usr/sbin/apparmor_parser --preprocess /etc/apparmor.d/bwrap-userns-restrict > "$evidence/effective-profile.txt"
python3 "$helper" fingerprint-files /etc/apparmor.d/bwrap-userns-restrict \
    /etc/apparmor.d/abi /etc/apparmor.d/tunables > "$evidence/profile-inputs.json"
stat /etc/apparmor.d/bwrap-userns-restrict > "$evidence/installed-profile.stat"
sudo /usr/sbin/apparmor_parser --replace --skip-cache /etc/apparmor.d/bwrap-userns-restrict
sudo cat /sys/kernel/security/apparmor/profiles > "$evidence/active-profiles.txt"
grep -Fx 'bwrap (enforce)' "$evidence/active-profiles.txt"
grep -Fx 'unpriv_bwrap (enforce)' "$evidence/active-profiles.txt"
sha256sum "$evidence/vendor-profile" "$evidence/effective-profile.txt" > "$evidence/profile.sha256"
sysctl kernel.apparmor_restrict_unprivileged_userns kernel.unprivileged_userns_clone > "$evidence/userns-restrictions.txt"
# Installation smoke only: this trusted probe uses a read-only host-root view.
# It does NOT qualify the native producer's minimal filesystem export boundary.
for namespace in user mnt pid net ipc uts; do readlink "/proc/self/ns/$namespace"; done > "$evidence/host-namespaces.txt"
/usr/bin/bwrap --unshare-user --unshare-pid --unshare-net --unshare-ipc --unshare-uts \
    --disable-userns --new-session --cap-drop ALL \
    --ro-bind / / --proc /proc --dev /dev -- /bin/sh -ec \
    'cat /proc/self/attr/current; for n in user mnt pid net ipc uts; do readlink /proc/self/ns/$n; done' \
    > "$evidence/installed-runtime.txt"
python3 "$helper" verify-smoke "$evidence/host-namespaces.txt" "$evidence/installed-runtime.txt"
# Preserve the complete pinned inventory and native TAP/skip results, targeting
# the installed path after Meson's BWRAP injection without altering assertions.
/usr/bin/python3.12 /usr/bin/meson introspect --tests "$build" > "$evidence/upstream-test-inventory.json"
set +e
env -i HOME="$work" PATH=/usr/bin:/bin LANG=C.UTF-8 NINJA=/usr/bin/ninja \
    /usr/bin/python3.12 /usr/bin/meson test -C "$build" --no-rebuild --num-processes 1 \
    --print-errorlogs --wrapper "/usr/bin/python3.12 $helper upstream-wrapper $build"
test_status=$?
set -e
cp "$build/meson-logs/"testlog* "$evidence/"
sha256sum --check --strict "$evidence/installed-bwrap.sha256"
exit "$test_status"
