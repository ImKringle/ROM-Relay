// Package dashboard provides the interactive operator console. It is only
// enabled when stdin and stdout are terminals; redirected/service runs retain
// deterministic plain logs.
package dashboard

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type PlatformRow struct {
	Platform                     string
	Games, Base, Patched, Files  int
	EncryptedCIA, UnencryptedCIA int
}

type Snapshot struct {
	PublicURL, AdminURL, Status, LastError string
	Platforms                              []PlatformRow
	Jobs                                   []JobProgress
	Games, Files, ActiveDownloads          int
	HTTPRequests, HTTPBytes, HTTPActive    int64
	DBOpen, DBInUse, DBIdle                int
	DBWait                                 int64
	WAL                                    bool
}

type JobProgress struct {
	Message     string
	Done, Total int64
	Error       string
}

type Dashboard struct {
	app                         *tview.Application
	header, activity, diag, log *tview.TextView
	platforms                   *tview.Table
	root                        tview.Primitive
	theme                       string
	started                     time.Time
	source                      func(context.Context) Snapshot
	quit                        func()
	stop                        chan struct{}
	done                        chan error
	stopOnce                    sync.Once
	running                     atomic.Bool
	jobsMu                      sync.RWMutex
	liveJobs                    map[string]string
	cpu                         cpuSampler
}

func TerminalAvailable(in, out *os.File) bool {
	if in == nil || out == nil {
		return false
	}
	inStat, inErr := in.Stat()
	outStat, outErr := out.Stat()
	return inErr == nil && outErr == nil && inStat.Mode()&os.ModeCharDevice != 0 && outStat.Mode()&os.ModeCharDevice != 0
}

func New(theme string) *Dashboard {
	d := &Dashboard{app: tview.NewApplication(), theme: theme, started: time.Now(), stop: make(chan struct{}), done: make(chan error, 1), liveJobs: map[string]string{}}
	accent := tcell.ColorAqua
	if theme == "corn" {
		accent = tcell.ColorYellow
	}
	d.header = panel(tview.NewTextView().SetDynamicColors(true), " ROM Relay ", accent)
	d.activity = panel(tview.NewTextView().SetDynamicColors(true).SetScrollable(true), " Activity / Jobs ", accent)
	d.diag = panel(tview.NewTextView().SetDynamicColors(true), " Runtime / HTTP / DB ", accent)
	d.log = panel(tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetMaxLines(1200), " Live Logs ", accent)
	d.platforms = tview.NewTable().SetFixed(1, 1).SetEvaluateAllRows(true)
	d.platforms.SetBorder(true).SetTitle(" Library by Platform ").SetBorderColor(accent).SetTitleColor(accent)
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter).SetText("[gray]Tab: focus  •  ↑/↓: scroll  •  End: latest logs  •  q / Ctrl+C: clean shutdown[-]")
	top := tview.NewFlex().AddItem(d.platforms, 0, 5, false).AddItem(d.activity, 0, 4, false).AddItem(d.diag, 0, 3, false)
	d.root = tview.NewFlex().SetDirection(tview.FlexRow).AddItem(d.header, 5, 0, false).AddItem(top, 0, 5, false).AddItem(d.log, 0, 6, true).AddItem(footer, 1, 0, false)
	d.app.SetRoot(d.root, true).EnableMouse(true).SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Rune() == 'q' || event.Rune() == 'Q' || event.Key() == tcell.KeyCtrlC {
			if d.quit != nil {
				go d.quit()
			} else {
				d.app.Stop()
			}
			return nil
		}
		return event
	})
	return d
}

func panel(view *tview.TextView, title string, color tcell.Color) *tview.TextView {
	view.SetBorder(true).SetTitle(title).SetBorderColor(color).SetTitleColor(color)
	return view
}

func (d *Dashboard) SetSource(source func(context.Context) Snapshot) { d.source = source }
func (d *Dashboard) SetQuit(quit func())                             { d.quit = quit }

// SetLiveJob implements presentation.LiveSink. Jobs are keyed so repeated
// progress updates replace one row instead of flooding the log window.
func (d *Dashboard) SetLiveJob(key, message string, done, total int64, active bool) {
	d.jobsMu.Lock()
	defer d.jobsMu.Unlock()
	if !active {
		delete(d.liveJobs, key)
		return
	}
	if total > 0 {
		message = renderProgress(message, done, total)
	}
	d.liveJobs[key] = message
}

func (d *Dashboard) Start() {
	go func() {
		d.running.Store(true)
		defer d.running.Store(false)
		d.done <- d.app.Run()
	}()
	go d.refreshLoop()
}

func (d *Dashboard) Stop() error {
	d.stopOnce.Do(func() {
		close(d.stop)
		d.app.Stop()
	})
	select {
	case err := <-d.done:
		return err
	case <-time.After(3 * time.Second):
		return errorsNew("dashboard did not stop within 3 seconds")
	}
}

func errorsNew(message string) error { return fmt.Errorf("%s", message) }

func (d *Dashboard) Write(p []byte) (int, error) {
	_, err := io.WriteString(d.log, tview.Escape(string(p)))
	if d.running.Load() {
		d.app.QueueUpdateDraw(func() { d.log.ScrollToEnd() })
	}
	return len(p), err
}

func (d *Dashboard) refreshLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.refresh()
		}
	}
}

func (d *Dashboard) refresh() {
	var snap Snapshot
	if d.source != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		snap = d.source(ctx)
		cancel()
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	cpu := d.cpu.sample()
	d.app.QueueUpdateDraw(func() {
		d.header.SetText(headerText(snap, time.Since(d.started)))
		d.renderPlatforms(snap.Platforms)
		d.jobsMu.RLock()
		live := make([]string, 0, len(d.liveJobs))
		for _, job := range d.liveJobs {
			live = append(live, tview.Escape(job))
		}
		d.jobsMu.RUnlock()
		sort.Strings(live)
		remoteJobs := make([]string, 0, len(snap.Jobs))
		for _, job := range snap.Jobs {
			line := job.Message
			if job.Total > 0 {
				line = renderProgress(line, job.Done, job.Total)
			}
			if job.Error != "" {
				line += " · " + job.Error
			}
			remoteJobs = append(remoteJobs, tview.Escape(line))
		}
		jobs := strings.Join(append(live, remoteJobs...), "\n")
		if jobs == "" {
			jobs = "[gray]No active background jobs[-]"
		}
		if snap.LastError != "" {
			jobs += "\n[red]Last error:[-] " + tview.Escape(snap.LastError)
		}
		d.activity.SetText(jobs)
		d.diag.SetText(fmt.Sprintf("[aqua]CPU[-] %.1f%%\n[aqua]Heap[-] %s / [aqua]Sys[-] %s\n[aqua]Goroutines[-] %d  [aqua]GC[-] %d\n\n[aqua]HTTP[-] %d req  %s out\n[aqua]Active HTTP[-] %d  [aqua]Downloads[-] %d\n\n[aqua]SQLite[-] open %d / in-use %d / idle %d\n[aqua]DB waits[-] %d  [aqua]WAL[-] %t\n[aqua]Catalog[-] %d games / %d files", cpu, bytes(mem.HeapAlloc), bytes(mem.Sys), runtime.NumGoroutine(), mem.NumGC, snap.HTTPRequests, bytes(uint64(snap.HTTPBytes)), snap.HTTPActive, snap.ActiveDownloads, snap.DBOpen, snap.DBInUse, snap.DBIdle, snap.DBWait, snap.WAL, snap.Games, snap.Files))
	})
}

func renderProgress(message string, done, total int64) string {
	const width = 18
	if total <= 0 {
		return message
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	filled := int(float64(done)*width/float64(total) + .5)
	return fmt.Sprintf("%s  %s%s  %5.1f%%", message, strings.Repeat("█", filled), strings.Repeat("░", width-filled), float64(done)*100/float64(total))
}

func headerText(snap Snapshot, uptime time.Duration) string {
	return fmt.Sprintf("[::b]ROM Relay[-]  [gray]uptime[-] %s  [gray]status[-] %s\n[gray]Public[-] %s\n[gray]Admin[-] %s",
		shortDuration(uptime),
		tview.Escape(value(snap.Status, "starting")),
		tview.Escape(value(snap.PublicURL, "initializing")),
		tview.Escape(value(snap.AdminURL, "initializing")),
	)
}

func (d *Dashboard) renderPlatforms(rows []PlatformRow) {
	d.platforms.Clear()
	headers := []string{"Platform", "Games", "Base", "Patched", "Files", "Enc", "Dec"}
	for column, header := range headers {
		d.platforms.SetCell(0, column, tview.NewTableCell("[::b]"+header).SetTextColor(tcell.ColorWhite).SetSelectable(false))
	}
	for row, item := range rows {
		values := []string{strings.ToUpper(item.Platform), fmt.Sprint(item.Games), fmt.Sprint(item.Base), fmt.Sprint(item.Patched), fmt.Sprint(item.Files), fmt.Sprint(item.EncryptedCIA), fmt.Sprint(item.UnencryptedCIA)}
		for column, cell := range values {
			d.platforms.SetCell(row+1, column, tview.NewTableCell(cell).SetTextColor(tcell.ColorLightGray))
		}
	}
}

func shortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return d.String()
	}
	return d.Truncate(time.Second).String()
}

func bytes(n uint64) string {
	const unit = uint64(1024)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), "KiB"
	for _, next := range []string{"MiB", "GiB", "TiB"} {
		value /= 1024
		if value < 1024 {
			break
		}
		suffix = next
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}

func value(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return tview.Escape(v)
}
