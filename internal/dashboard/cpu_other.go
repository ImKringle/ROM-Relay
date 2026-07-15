//go:build !windows

package dashboard

type cpuSampler struct{}

func (s *cpuSampler) sample() float64 { return 0 }
