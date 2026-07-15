package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestBytes(t *testing.T) {
	for input, want := range map[uint64]string{0: "0 B", 1024: "1.0 KiB", 1024 * 1024: "1.0 MiB"} {
		if got := bytes(input); got != want {
			t.Errorf("bytes(%d)=%q want %q", input, got, want)
		}
	}
}

func TestCtrlCRequestsCleanShutdownWithoutStoppingDashboardDirectly(t *testing.T) {
	d := New("default")
	called := make(chan struct{}, 1)
	d.SetQuit(func() { called <- struct{}{} })
	if event := d.app.GetInputCapture()(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModCtrl)); event != nil {
		t.Fatal("Ctrl+C was passed through to tview instead of being consumed")
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("Ctrl+C did not enter the clean shutdown callback")
	}
}

func TestLiveJobsReplaceAndClear(t *testing.T) {
	d := New("default")
	d.SetLiveJob("scan", "Scanning", 1, 4, true)
	d.SetLiveJob("scan", "Scanning", 3, 4, true)
	if len(d.liveJobs) != 1 || d.liveJobs["scan"] != "Scanning  ██████████████░░░░   75.0%" {
		t.Fatalf("unexpected live jobs: %#v", d.liveJobs)
	}
	d.SetLiveJob("scan", "", 0, 0, false)
	if len(d.liveJobs) != 0 {
		t.Fatalf("job was not cleared: %#v", d.liveJobs)
	}
}

func TestSplitPaneLayoutRendersAtStandardTerminalSize(t *testing.T) {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(120, 40)
	d := New("default")
	d.root.SetRect(0, 0, 120, 40)
	d.root.Draw(screen)
	screen.Show()
	cells, _, _ := screen.GetContents()
	var rendered strings.Builder
	for _, cell := range cells {
		rendered.Write(cell.Bytes)
	}
	text := rendered.String()
	for _, title := range []string{"ROM Relay", "Library by Platform", "Activity / Jobs", "Runtime / HTTP / DB", "Live Logs", "clean shutdown"} {
		if !strings.Contains(text, title) {
			t.Errorf("split dashboard did not render %q", title)
		}
	}
}

func TestHeaderKeepsAdminTokenOnItsOwnLine(t *testing.T) {
	const token = "abcdefghijklmnopqrstuvwxyz012345"
	got := headerText(Snapshot{
		PublicURL: "http://192.168.1.100:8080/?token=viewer-token",
		AdminURL:  "http://192.168.1.100:8080/admin/patches?admin_token=" + token,
		Status:    "serving",
	}, time.Minute)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("header has %d lines, want 3: %q", len(lines), got)
	}
	if !strings.Contains(lines[2], "admin_token="+token) {
		t.Fatalf("admin URL token missing from dedicated line: %q", lines[2])
	}
}
