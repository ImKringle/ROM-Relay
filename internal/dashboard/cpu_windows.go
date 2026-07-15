//go:build windows

package dashboard

import (
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

type cpuSampler struct {
	lastCPU  int64
	lastWall time.Time
}

func (s *cpuSampler) sample() float64 {
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user) != nil {
		return 0
	}
	now := time.Now()
	cpu := kernel.Nanoseconds() + user.Nanoseconds()
	if s.lastWall.IsZero() {
		s.lastWall, s.lastCPU = now, cpu
		return 0
	}
	wall := now.Sub(s.lastWall).Nanoseconds()
	delta := cpu - s.lastCPU
	s.lastWall, s.lastCPU = now, cpu
	if wall <= 0 || delta < 0 {
		return 0
	}
	return float64(delta) * 100 / float64(wall) / float64(runtime.NumCPU())
}
