package testenv

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nsqio/go-nsq"
)

// nsqd runs in its own process, built once per test binary from the nsqd
// tool in go.mod. Run inside a -race test binary, nsqd v1.3.0 has races of
// its own: an ephemeral channel's background deletion can overlap Exit.
var (
	nsqdOnce sync.Once
	nsqdBin  string
	nsqdDir  string
	nsqdErr  error
)

func nsqdBinary() (string, error) {
	nsqdOnce.Do(func() {
		if nsqdDir, nsqdErr = os.MkdirTemp("", "testenv-nsqd-"); nsqdErr != nil {
			return
		}
		_, file, _, _ := runtime.Caller(0)
		bin := filepath.Join(nsqdDir, "nsqd")
		cmd := exec.Command("go", "build", "-o", bin, "github.com/nsqio/nsq/apps/nsqd")
		cmd.Dir = filepath.Join(filepath.Dir(file), "..", "..")
		if out, err := cmd.CombinedOutput(); err != nil {
			nsqdErr = fmt.Errorf("build nsqd: %v: %s", err, out)
			return
		}
		nsqdBin = bin
	})
	return nsqdBin, nsqdErr
}

// NSQD starts an nsqd on a random port and returns its TCP address and a
// function that publishes a message to a topic.
func NSQD(t testing.TB) (tcpAddr string, publish func(topic string, body []byte)) {
	t.Helper()
	tcpAddr, publish, _ = NSQDAt(t, "127.0.0.1:0")
	return tcpAddr, publish
}

// NSQDAt is NSQD listening on addr, and also returns a function that shuts
// the nsqd down (it is shut down at cleanup either way).
func NSQDAt(t testing.TB, addr string) (tcpAddr string, publish func(topic string, body []byte), stop func()) {
	t.Helper()
	bin, err := nsqdBinary()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin,
		"--tcp-address="+addr,
		"--http-address=127.0.0.1:0",
		"--https-address=",
		"--broadcast-address=127.0.0.1",
		"--data-path="+t.TempDir())
	out, w := io.Pipe()
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		t.Fatalf("start nsqd: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		w.Close()
		close(exited)
	}()

	// It logs "TCP: listening on <addr>" once it accepts connections; if it
	// exits first, failed gets what it logged.
	listening, failed := make(chan string, 1), make(chan string, 1)
	go func() {
		var log strings.Builder
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if _, a, ok := strings.Cut(sc.Text(), "TCP: listening on "); ok {
				listening <- a
				io.Copy(io.Discard, out)
				return
			}
			log.WriteString(sc.Text() + "\n")
		}
		failed <- log.String()
	}()

	var once sync.Once
	var producer *nsq.Producer
	stop = func() {
		once.Do(func() {
			if producer != nil {
				producer.Stop()
			}
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				cmd.Process.Kill()
				<-exited
			}
		})
	}
	t.Cleanup(stop)

	select {
	case tcpAddr = <-listening:
	case log := <-failed:
		t.Fatalf("nsqd exited before listening:\n%s", log)
	case <-time.After(10 * time.Second):
		t.Fatal("nsqd did not start listening")
	}

	if producer, err = nsq.NewProducer(tcpAddr, nsq.NewConfig()); err != nil {
		t.Fatal(err)
	}
	producer.SetLogger(nil, nsq.LogLevelError)
	return tcpAddr, func(topic string, body []byte) {
		if err := producer.Publish(topic, body); err != nil {
			t.Errorf("nsqd publish: %v", err)
		}
	}, stop
}
