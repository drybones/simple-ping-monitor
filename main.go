// Command pingmon watches a network connection by pinging once a second and
// shows the results in a local web page.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/drybones/simple-ping-monitor/internal/logfile"
	"github.com/drybones/simple-ping-monitor/internal/monitor"
	"github.com/drybones/simple-ping-monitor/internal/probe"
	"github.com/drybones/simple-ping-monitor/internal/server"
)

const usage = `pingmon: ping once a second and watch the results in your browser.

Usage:
  pingmon [flags]              monitor until Ctrl-C
  pingmon view [flags] FILE    browse a recorded session

By default pingmon pings your router (found automatically) and 8.8.8.8, logs
every result to ~/pingmon-logs, and opens http://localhost:8080.

Flags:
`

type targetList []monitor.Target

func (t *targetList) String() string { return fmt.Sprint(*t) }

func (t *targetList) Set(v string) error {
	name, host, ok := strings.Cut(v, "=")
	if !ok {
		name, host = v, v
	}
	if name == "" || host == "" {
		return errors.New("want NAME=HOST or HOST")
	}
	*t = append(*t, monitor.Target{Name: name, Host: host})
	return nil
}

func main() {
	args := os.Args[1:]
	var err error
	if len(args) > 0 && args[0] == "view" {
		err = view(args[1:])
	} else {
		err = run(args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pingmon:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fl := flag.NewFlagSet("pingmon", flag.ContinueOnError)
	fl.Usage = func() {
		fmt.Fprint(fl.Output(), usage)
		fl.PrintDefaults()
	}
	var targets targetList
	fl.Var(&targets, "target", "`NAME=HOST` to ping; repeatable (default internet=8.8.8.8)")
	noGateway := fl.Bool("no-gateway", false, "don't ping the router")
	interval := fl.Duration("interval", time.Second, "time between pings")
	lossAfter := fl.Duration("loss-after", 5*time.Second, "count a ping as lost after this long without a reply")
	good := fl.Float64("good", monitor.DefaultThresholds.GoodMs, "RTT below this is good (ms)")
	warn := fl.Float64("warn", monitor.DefaultThresholds.WarnMs, "RTT at or above this is high (ms)")
	severe := fl.Float64("severe", monitor.DefaultThresholds.SevereMs, "RTT at or above this is severe (ms)")
	port := fl.Int("port", 8080, "web UI port (the next free one is used if taken)")
	logDir := fl.String("log-dir", defaultLogDir(), "where to write session logs")
	noOpen := fl.Bool("no-open", false, "don't open the browser")
	simulate := fl.Bool("simulate", false, "use a simulated flaky network instead of real pings")
	backfill := fl.Duration("sim-backfill", 0, "with -simulate, start this far in the past")
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(targets) == 0 {
		targets = targetList{{Name: "internet", Host: "8.8.8.8"}}
	}
	if *interval < 100*time.Millisecond {
		return errors.New("-interval must be at least 100ms")
	}
	if !(*good <= *warn && *warn <= *severe) {
		return errors.New("thresholds must satisfy -good <= -warn <= -severe")
	}

	if !*noGateway {
		gw := "192.168.1.1"
		if !*simulate {
			var err error
			if gw, err = probe.DefaultGateway(); err != nil {
				fmt.Fprintf(os.Stderr, "pingmon: not pinging the router: %v\n", err)
			}
		}
		if gw != "" {
			targets = append(targetList{{Name: "router", Host: gw, Gateway: true}}, targets...)
		}
	}

	now := time.Now()
	cfg := monitor.Config{
		Start:      now,
		Interval:   *interval,
		LossAfter:  *lossAfter,
		Thresholds: monitor.Thresholds{GoodMs: *good, WarnMs: *warn, SevereMs: *severe},
		Targets:    targets,
	}
	sources := make([]probe.Source, len(targets))
	for i, t := range targets {
		if *simulate {
			base := 22.0
			if t.Gateway {
				base = 2
			}
			sources[i] = &probe.Sim{Epoch: now, Interval: *interval, BaseMs: base, Backfill: *backfill, Seed: now.UnixNano()}
			continue
		}
		p, err := probe.NewICMP(t.Host, *interval)
		if err != nil {
			return err
		}
		if p.Addr.String() != t.Host {
			targets[i].Host = fmt.Sprintf("%s (%s)", t.Host, p.Addr)
		}
		sources[i] = p
	}
	if *simulate {
		cfg.Start = now.Add(-*backfill)
	}

	if err := os.MkdirAll(*logDir, 0o755); err != nil {
		return err
	}
	name := "pingmon-" + cfg.Start.Format("2006-01-02_150405")
	if *simulate {
		name += "-sim"
	}
	logPath := filepath.Join(*logDir, name+".jsonl")
	log, err := logfile.Create(logPath, cfg)
	if err != nil {
		return err
	}
	defer log.Close()

	m := monitor.New(cfg)
	m.OnSample = func(s monitor.Sample) { log.Sample(s) }

	ln, err := listen(*port)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String()
	srv := &http.Server{Handler: (&server.Server{Monitor: m}).Handler()}
	go srv.Serve(ln)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	events := make(chan probe.Event, 1024)
	errs := make(chan error, len(sources))
	for i, src := range sources {
		go func() {
			if err := src.Run(ctx, i, events); err != nil {
				errs <- fmt.Errorf("%s: %w", targets[i].Name, err)
			}
		}()
	}
	go m.Run(ctx, events)

	fmt.Printf("pingmon: pinging %s every %s\n", describeTargets(targets), *interval)
	fmt.Printf("  web UI:  %s\n  logging: %s\n  press Ctrl-C to stop\n\n", url, logPath)
	if !*noOpen {
		openBrowser(url)
	}

	var runErr error
	term := newTerminal(os.Stdout, cfg)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case runErr = <-errs:
			break loop
		case now := <-tick.C:
			term.update(m.Summary(now))
			log.Flush()
		}
	}
	stop()
	term.clear()

	sum := m.Summary(time.Now())
	log.Summary(sum)
	shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	srv.Shutdown(shutdown)

	fmt.Println()
	printSummary(os.Stdout, cfg, sum)
	fmt.Printf("\nLog saved to %s\nReview it later with: pingmon view %s\n", logPath, shellQuote(logPath))
	return runErr
}

func view(args []string) error {
	fl := flag.NewFlagSet("pingmon view", flag.ContinueOnError)
	port := fl.Int("port", 8080, "web UI port (the next free one is used if taken)")
	noOpen := fl.Bool("no-open", false, "don't open the browser")
	fl.Usage = func() {
		fmt.Fprint(fl.Output(), "Usage: pingmon view [flags] FILE\n\nFlags:\n")
		fl.PrintDefaults()
	}
	if err := fl.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fl.NArg() != 1 {
		fl.Usage()
		return errors.New("need exactly one log file")
	}
	path := fl.Arg(0)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	cfg, samples, err := logfile.Read(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	m := monitor.NewReplay(cfg, samples)

	ln, err := listen(*port)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String()
	srv := &http.Server{Handler: (&server.Server{Monitor: m, Label: filepath.Base(path)}).Handler()}
	go srv.Serve(ln)

	printSummary(os.Stdout, cfg, m.Summary(time.Time{}))
	fmt.Printf("\nViewing %s at %s (Ctrl-C to stop)\n", path, url)
	if !*noOpen {
		openBrowser(url)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	return srv.Close()
}

// listen binds to localhost only: the page is for the machine running it.
func listen(port int) (net.Listener, error) {
	var err error
	for p := port; p < port+10; p++ {
		var ln net.Listener
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			return ln, nil
		}
	}
	return nil, err
}

func defaultLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "pingmon-logs"
	}
	return filepath.Join(home, "pingmon-logs")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	_ = cmd.Start()
}

func describeTargets(ts []monitor.Target) string {
	var parts []string
	for _, t := range ts {
		parts = append(parts, fmt.Sprintf("%s (%s)", t.Name, t.Host))
	}
	return strings.Join(parts, " and ")
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\"\\$`") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
