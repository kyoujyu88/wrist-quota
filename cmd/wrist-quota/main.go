// Command wrist-quota collects AI tool usage limits on this machine and
// sends notifications about them to a phone and watch through ntfy.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/kyoujyu88/wrist-quota/internal/app"
	"github.com/kyoujyu88/wrist-quota/internal/claudesettings"
	"github.com/kyoujyu88/wrist-quota/internal/collector/claudecode"
	"github.com/kyoujyu88/wrist-quota/internal/collector/codex"
	"github.com/kyoujyu88/wrist-quota/internal/quota"
	"github.com/kyoujyu88/wrist-quota/internal/store"
	"github.com/kyoujyu88/wrist-quota/internal/web"
)

var version = "dev"

const usage = `wrist-quota — AI の利用枠をスマートウォッチへ通知する常駐ツール

使い方:
  wrist-quota [run]        常駐して収集・通知し、設定画面を配信する
  wrist-quota status       いま取得できる利用枠を表示して終了する
  wrist-quota statusline   Claude Code のステータスライン用コマンド（設定画面から登録する）
  wrist-quota version      バージョンを表示する
`

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "run":
		err = run()
	case "status":
		err = status()
	case "statusline":
		err = statusline(os.Stdin, os.Stdout)
	case "version", "-v", "--version":
		fmt.Println("wrist-quota", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wrist-quota:", err)
		os.Exit(1)
	}
}

func options() (app.Options, error) {
	dir, err := store.DefaultDir()
	if err != nil {
		return app.Options{}, err
	}
	settings, err := claudesettings.DefaultPath()
	if err != nil {
		return app.Options{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	return app.Options{Dir: dir, CodexHome: codex.DefaultHome(), ClaudeSettings: settings, Executable: exe}, nil
}

func run() error {
	opts, err := options()
	if err != nil {
		return err
	}
	a, err := app.New(opts)
	if err != nil {
		return err
	}
	token, err := web.NewToken()
	if err != nil {
		return err
	}
	listeners, err := web.Listen(a.Listen())
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Handler:           (&web.Server{Backend: a, Token: token}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	for _, l := range listeners {
		go func(l net.Listener) {
			if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("settings screen stopped: %v", err)
			}
		}(l)
	}
	_, port, _ := net.SplitHostPort(listeners[0].Addr().String())
	log.Printf("設定画面: http://127.0.0.1:%s/", port)

	a.Run(ctx)

	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// status prints what can be collected right now without starting the daemon
// or sending anything.
func status() error {
	opts, err := options()
	if err != nil {
		return err
	}
	var windows []quota.Window
	snap, err := claudecode.ReadSnapshot(store.Store{Dir: opts.Dir}.SnapshotPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude-code:", err)
	} else if snap != nil {
		windows = append(windows, snap.Windows...)
	}
	ws, err := (&codex.Collector{Home: opts.CodexHome}).Collect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "codex:", err)
	}
	windows = append(windows, ws...)
	if len(windows) == 0 {
		fmt.Println("利用枠はまだ取得できていません")
		return nil
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].Key() < windows[j].Key() })
	now := time.Now()
	for _, w := range windows {
		reset := "リセット時刻不明"
		if w.ResetsAt != nil {
			reset = "リセット " + w.ResetsAt.Local().Format("1/2 15:04")
		}
		note := ""
		if w.StatusAt(now) == quota.StatusResetEstimated {
			note = "（リセット済みと推定）"
		}
		fmt.Printf("%-16s 残り %3d%%  %s  観測 %s%s\n", w.Key(), int(math.Round(w.Remaining())), reset,
			w.ObservedAt.Local().Format("1/2 15:04"), note)
	}
	return nil
}

// statusline is run by Claude Code on every status line refresh. It must be
// quick: it stores the quota numbers for the daemon and sends nothing.
func statusline(in io.Reader, out io.Writer) error {
	input, err := io.ReadAll(io.LimitReader(in, 4<<20))
	if err != nil {
		return err
	}
	dir, err := store.DefaultDir()
	if err != nil {
		return err
	}
	st := store.Store{Dir: dir}
	now := time.Now()

	windows, parseErr := claudecode.Parse(input, now)
	if parseErr == nil && len(windows) > 0 {
		writeSnapshot(st, claudecode.Snapshot{ObservedAt: now.UTC(), Windows: windows})
	}

	// An existing status line keeps its display: it gets the same input and
	// its output is passed through untouched.
	if cfg, err := st.LoadConfigReadOnly(); err == nil && cfg.Statusline.Wrapped != "" {
		return runWrapped(cfg.Statusline.Wrapped, input, out)
	}

	if len(windows) == 0 {
		fmt.Fprintln(out, "wrist-quota: 利用枠は未取得")
		return nil
	}
	parts := make([]string, len(windows))
	for i, w := range windows {
		parts[i] = fmt.Sprintf("%s 残り%d%%", w.Window, int(math.Round(w.Remaining())))
	}
	fmt.Fprintln(out, strings.Join(parts, " · "))
	return nil
}

func writeSnapshot(st store.Store, snap claudecode.Snapshot) {
	// Failing to store must not break the status line.
	if err := st.SaveSnapshot(snap); err != nil {
		fmt.Fprintln(os.Stderr, "wrist-quota:", err)
	}
}

func runWrapped(command string, input []byte, out io.Writer) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
