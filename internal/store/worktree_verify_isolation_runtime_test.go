package store

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func nativeIsolationFixtureScratch(t *testing.T, required bool) (string, *bool) {
	t.Helper()
	preserve := new(bool)
	if !required {
		return t.TempDir(), preserve
	}
	root, err := os.MkdirTemp("", "concord-native-isolation-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if *preserve {
			t.Logf("unqualified teardown; scratch retained at %s", root)
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root, preserve
}

func nativeIsolationRuntimeTarget(t *testing.T) *nativeTargetOraclePlan {
	t.Helper()
	if os.Getenv("OC_NATIVE_ISOLATION_REQUIRED") != "1" {
		t.Skip("fixed launcher/profile/runtime qualification runs on disposable CI with OC_NATIVE_ISOLATION_REQUIRED=1")
	}
	// This static check precedes every possible launch, including on a host
	// accidentally given the CI requirement. It never invokes --version.
	if err := nativeIsolationLauncherVersion(nativeIsolationLauncher); err != nil {
		t.Fatal(err)
	}
	m, c := nativeVitestManifestForTest()
	m.Isolation = nativeIsolationLoopback
	if err := validateNativeTargetManifest(m, c); err != nil {
		t.Fatal(err)
	}
	pair, _ := nativeTargetPairOf(m)
	e, err := resolveNativeTargetRuntime(context.Background(), m, pair)
	if err != nil {
		t.Fatal(err)
	}
	return &nativeTargetOraclePlan{Recipe: m, Environment: e}
}

func nativeIsolationBindRuntimeProbe(t *testing.T, target *nativeTargetOraclePlan, snapshot string) {
	t.Helper()
	for name, body := range map[string]string{"package.json": `{"name":"isolation-runtime-probe"}`, "bun.lock": `{"lockfileVersion":1,"workspaces":{"":{"name":"isolation-runtime-probe"}},"packages":{}}`} {
		if err := os.WriteFile(filepath.Join(snapshot, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := nativeIsolationBindTargetExports(target, snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestNativeIsolationRuntimeBoundary(t *testing.T) {
	target := nativeIsolationRuntimeTarget(t)
	root, preserve := nativeIsolationFixtureScratch(t, true)
	if err := nativeScratchDirectories(root); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(root, "snapshot")
	if err := os.MkdirAll(filepath.Join(snapshot, "node_modules"), 0700); err != nil {
		t.Fatal(err)
	}
	target.Recipe.Runner = nativeTargetRunnerCheck
	if err := nativeTargetInstallRunner(target, root, OracleControl{}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(net.IPv4(127, 0, 0, 1).String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	abstractName := "@" + filepath.Base(root) + "-ipc"
	abstract, err := net.Listen("unix", abstractName)
	if err != nil {
		t.Fatal(err)
	}
	defer abstract.Close()
	pathSocket := filepath.Join(t.TempDir(), "host.sock")
	pathname, err := net.Listen("unix", pathSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer pathname.Close()
	code := fmt.Sprintf(`import assert from 'node:assert/strict';
import net from 'node:net';
import fs from 'node:fs';
import os from 'node:os';
import {spawnSync} from 'node:child_process';
const loopback=[127,0,0,1].join('.');
const server=net.createServer(socket=>socket.end('private'));
await new Promise((resolve,reject)=>server.listen(0,loopback,resolve).once('error',reject));
const received=await new Promise((resolve,reject)=>{let data='';const c=net.connect(server.address().port,loopback);c.on('data',v=>data+=v);c.on('end',()=>resolve(data));c.on('error',reject)});
assert.equal(received,'private'); await new Promise(resolve=>server.close(resolve));
async function denied(host,port){await new Promise((resolve,reject)=>{const c=net.connect(port,host);c.setTimeout(500);c.on('connect',()=>{c.destroy();reject(Error('host/external network reachable'))});c.on('error',resolve);c.on('timeout',()=>{c.destroy();resolve()})})}
assert.ok(Object.values(os.networkInterfaces()).flat().every(({internal})=>internal),'non-loopback interface exposed');
await denied(loopback,%d); await denied([1,1,1,1].join('.'),443);
for(const path of [%q,%q])await new Promise((resolve,reject)=>{const c=net.connect({path});c.on('connect',()=>{c.destroy();reject(Error('host IPC reachable'))});c.on('error',resolve)});
for(const fd of fs.readdirSync('/proc/self/fd')){if(Number(fd)<=2)continue;let info;try{info=fs.readFileSync('/proc/self/fdinfo/'+fd,'utf8')}catch(error){if(error.code==='ENOENT')continue;throw error}const match=/flags:\s*([0-7]+)/.exec(info);assert.ok(match);assert.ok(Number.parseInt(match[1],8)&0o2000000,'inherited descriptor '+fd)}
for(const fd of [1,2])assert.match(fs.readlinkSync('/proc/self/fd/'+fd),/^pipe:/);
for(const file of ['/etc/hosts',process.cwd()+'/probe.mjs','/forbidden'])assert.throws(()=>fs.writeFileSync(file,'forbidden'));
assert.throws(()=>fs.readFileSync('/run/docker.sock')); assert.throws(()=>fs.readFileSync('/etc/resolv.conf'));
fs.writeFileSync(os.tmpdir()+'/private-output','ok');
fs.writeFileSync('/dev/shm/private-output','ok');
const child=spawnSync(process.execPath,['-e','process.stdout.write(require("node:os").homedir())'],{env:{PATH:process.env.PATH},encoding:'utf8'});
assert.equal(child.status,0,child.stderr);assert.equal(child.stdout,process.env.HOME);
assert.equal(os.hostname(),'sandbox');
assert.equal(fs.readFileSync('/proc/self/attr/apparmor/current','utf8').trim(),'bwrap//&unpriv_bwrap (enforce)');
`, port, "\x00"+strings.TrimPrefix(abstractName, "@"), pathSocket)
	script := filepath.Join(snapshot, "probe.mjs")
	if err := os.WriteFile(script, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	argv := []string{target.Environment.Node, script}
	nativeIsolationBindRuntimeProbe(t, target, snapshot)
	plan, err := nativeTargetIsolationExport(target, root, snapshot, snapshot, argv)
	if err != nil {
		t.Fatal(err)
	}
	capture := &nativeStreamCapture{complete: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	exit, err := runNativeOracleStage(ctx, snapshot, argv, nativeTargetControlledEnv(target, root), nil, capture, nil, plan)
	if !capture.isolationReaped {
		*preserve = true
	}
	if err != nil || exit != 0 || !capture.complete {
		t.Fatalf("boundary probe exit=%d err=%v stdout=%s stderr=%s", exit, err, capture.stdout, capture.stderr)
	}
	if data, err := os.ReadFile(filepath.Join(root, "tmp", "private-output")); err != nil || string(data) != "ok" {
		t.Fatalf("private scratch write missing: %v", err)
	}
}

func TestNativeIsolationRuntimeDescendants(t *testing.T) {
	target := nativeIsolationRuntimeTarget(t)
	target.Recipe.Runner = nativeTargetRunnerCheck
	for _, mode := range []string{"completion", "cancel", "timeout", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			root, preserve := nativeIsolationFixtureScratch(t, true)
			if err := nativeScratchDirectories(root); err != nil {
				t.Fatal(err)
			}
			snapshot := filepath.Join(root, "snapshot")
			if err := os.MkdirAll(filepath.Join(snapshot, "node_modules"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := nativeTargetInstallRunner(target, root, OracleControl{}); err != nil {
				t.Fatal(err)
			}
			token := filepath.Base(root) + "-detached-witness"
			marker := filepath.Join(root, "tmp", "child-ready")
			child := fmt.Sprintf(`require('node:fs').writeFileSync(%q,'ready');setInterval(()=>{},1000)`, marker)
			body := fmt.Sprintf(`import fs from 'node:fs';import {spawn} from 'node:child_process';
const child=spawn(process.execPath,['-e',%q,%q],{detached:true,stdio:'ignore',env:{PATH:process.env.PATH}});child.unref();
while(!fs.existsSync(%q))await new Promise(resolve=>setTimeout(resolve,10));
`, child, token, marker)
			if mode == "overflow" {
				body += `process.stdout.write('x'.repeat(3*1024*1024));setInterval(()=>{},1000);`
			} else if mode != "completion" {
				body += `setInterval(()=>{},1000);`
			}
			script := filepath.Join(snapshot, "descendants.mjs")
			if err := os.WriteFile(script, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			argv := []string{target.Environment.Node, script}
			nativeIsolationBindRuntimeProbe(t, target, snapshot)
			plan, err := nativeTargetIsolationExport(target, root, snapshot, snapshot, argv)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if mode == "timeout" {
				var timeoutCancel context.CancelFunc
				ctx, timeoutCancel = context.WithTimeout(ctx, 3*time.Second)
				defer timeoutCancel()
			}
			if mode == "cancel" {
				go func() {
					for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
						if _, err := os.Stat(marker); err == nil {
							cancel()
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
					cancel()
				}()
			}
			capture := &nativeStreamCapture{complete: true}
			exit, runErr := runNativeOracleStage(ctx, snapshot, argv, nativeTargetControlledEnv(target, root), nil, capture, nil, plan)
			if !capture.isolationReaped {
				*preserve = true
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("descendant did not start: %v; run=%v stderr=%s", err, runErr, capture.stderr)
			}
			if mode == "completion" && (runErr != nil || exit != 0) {
				t.Fatalf("completion exit=%d err=%v stderr=%s", exit, runErr, capture.stderr)
			}
			if mode != "completion" && runErr == nil {
				t.Fatalf("%s did not abort stage", mode)
			}
			if !capture.isolationEstablished || !capture.isolationReaped {
				t.Fatalf("%s did not prove teardown completion: %v", mode, runErr)
			}
			if mode != "overflow" && !capture.complete {
				t.Fatalf("%s lost output: %v", mode, runErr)
			}
			entries, err := os.ReadDir("/proc")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
				if err == nil && strings.Contains(string(raw), token) {
					t.Fatalf("detached descendant survived %s: pid=%s", mode, entry.Name())
				}
			}
		})
	}
}

func TestNativeIsolationLoaderCache(t *testing.T) {
	entries, err := nativeIsolationHostLoaderCache()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("host loader cache supplied no amd64 entries")
	}
	for _, raw := range [][]byte{nil, []byte("ld.so-1.7.0"), make([]byte, 48), []byte("glibc-ld.so.cache1.1")} {
		if _, err := nativeIsolationLoaderCache(raw); err == nil {
			t.Fatal("malformed cache admitted")
		}
	}
	name, err := nativeTargetResolvedTool("bash")
	if err != nil {
		t.Fatal(err)
	}
	if err := nativeIsolationCacheAgrees([]nativeIsolationCacheEntry{{Path: name, HWCaps: 1}}, name); err == nil {
		t.Fatal("unqualified cache alternate admitted")
	}
}

func TestNativeIsolationCallerProcess(t *testing.T) {
	root := os.Getenv("TEST_CONCORD_NATIVE_ISOLATION_CALLER_ROOT")
	if root == "" {
		t.Skip("private caller-death fixture role")
	}
	target := nativeIsolationRuntimeTarget(t)
	target.Recipe.Runner = nativeTargetRunnerCheck
	if err := nativeTargetInstallRunner(target, root, OracleControl{}); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(root, "snapshot")
	argv := []string{target.Environment.Node, filepath.Join(snapshot, "caller.mjs")}
	nativeIsolationBindRuntimeProbe(t, target, snapshot)
	plan, err := nativeTargetIsolationExport(target, root, snapshot, snapshot, argv)
	if err != nil {
		t.Fatal(err)
	}
	capture := &nativeStreamCapture{complete: true}
	_, err = runNativeOracleStage(context.Background(), snapshot, argv, nativeTargetControlledEnv(target, root), nil, capture, nil, plan)
	t.Fatalf("caller fixture returned before caller death: %v; %s", err, capture.stderr)
}

func TestNativeIsolationRuntimeCallerDeath(t *testing.T) {
	_ = nativeIsolationRuntimeTarget(t)
	root, preserve := nativeIsolationFixtureScratch(t, true)
	if err := nativeScratchDirectories(root); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(root, "snapshot")
	if err := os.MkdirAll(filepath.Join(snapshot, "node_modules"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "tmp", "caller-child-ready")
	token := filepath.Base(root) + "-caller-death-descendant"
	child := fmt.Sprintf(`require('node:fs').writeFileSync(%q,'ready');setInterval(()=>{},1000)`, marker)
	body := fmt.Sprintf(`import {spawn} from 'node:child_process';const child=spawn(process.execPath,['-e',%q,%q],{detached:true,stdio:'ignore'});child.unref();setInterval(()=>{},1000);`, child, token)
	if err := os.WriteFile(filepath.Join(snapshot, "caller.mjs"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/proc/self/exe", "-test.run=^TestNativeIsolationCallerProcess$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "TEST_CONCORD_NATIVE_ISOLATION_CALLER_ROOT="+root)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	*preserve = true
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	ready := false
	for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(marker); err == nil {
			ready = true
			break
		}
		select {
		case err := <-done:
			waited = true
			t.Fatalf("caller exited before namespace child started: %v\n%s", err, output.String())
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("caller did not reach gated workload; scratch retained")
	}
	children, err := nativeIsolationProcessChildren(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := -1
	for _, pid := range children {
		args, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err == nil && bytes.Contains(args, []byte(nativeSupervisorArg)) {
			supervisor = pid
			break
		}
	}
	if supervisor < 2 {
		t.Fatal("no owned supervisor after workload startup")
	}
	pidfd, err := unix.PidfdOpen(supervisor, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pidfd)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done
	waited = true
	poll := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	if n, err := unix.Poll(poll, 15000); err != nil || n != 1 || poll[0].Revents&unix.POLLIN == 0 {
		t.Fatalf("cleanup owner did not terminate after caller death: %v; scratch retained", err)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err == nil && bytes.Contains(raw, []byte(token)) {
			t.Fatalf("detached child survived caller death: pid=%s", entry.Name())
		}
	}
	*preserve = false
}

func nativeIsolationProcessChildren(pid int) ([]int, error) {
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return nil, err
	}
	var children []int
	for _, task := range tasks {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/children", pid, task.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, value := range strings.Fields(string(raw)) {
			child, err := strconv.Atoi(value)
			if err != nil {
				return nil, err
			}
			children = append(children, child)
		}
	}
	return children, nil
}

func TestNativeIsolationProcessChildrenAcrossThreads(t *testing.T) {
	type child struct {
		pid int
		err error
	}
	started := make(chan child, 2)
	done := make(chan struct{}, 2)
	stop := make(chan struct{})
	for range 2 {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			defer func() { done <- struct{}{} }()
			cmd := exec.Command("/bin/sleep", "30")
			if err := cmd.Start(); err != nil {
				started <- child{err: err}
				return
			}
			started <- child{pid: cmd.Process.Pid}
			<-stop
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}()
	}
	defer func() { close(stop); <-done; <-done }()
	var pids []int
	for range 2 {
		c := <-started
		if c.err != nil {
			t.Fatal(c.err)
		}
		pids = append(pids, c.pid)
	}
	children, err := nativeIsolationProcessChildren(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		if !slices.Contains(children, pid) {
			t.Fatalf("child of a Go runtime thread was omitted: %d", pid)
		}
	}
}
