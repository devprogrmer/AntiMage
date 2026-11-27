package nodeagent

import (
	"sync"
	"time"
)

type networkInfrastructure struct {
	SampledAt       time.Time                        `json:"sampled_at"`
	IntervalSeconds float64                          `json:"interval_seconds"`
	Rates           map[string]float64               `json:"counter_rates_per_second,omitempty"`
	Interfaces      []networkInterfaceInfrastructure `json:"interfaces"`
}

type networkInterfaceInfrastructure struct {
	Name string `json:"name"`
	MTU  uint64 `json:"mtu"`
}

// Each sampler belongs to one node instance. The first sample has no rates.
type networkInfrastructureSampler struct {
	mu       sync.Mutex
	previous map[string]uint64
	at       time.Time
	cached   networkInfrastructure
}

func (s *networkInfrastructureSampler) Sample(now time.Time) (networkInfrastructure, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && now.Sub(s.at) < time.Second {
		return s.cached, nil
	}
	counters, interfaces, err := readNetworkInfrastructure()
	if err != nil {
		return networkInfrastructure{}, err
	}
	return s.observeLocked(now, counters, interfaces), nil
}

func (s *networkInfrastructureSampler) observe(now time.Time, counters map[string]uint64, interfaces []networkInterfaceInfrastructure) networkInfrastructure {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observeLocked(now, counters, interfaces)
}

func (s *networkInfrastructureSampler) observeLocked(now time.Time, counters map[string]uint64, interfaces []networkInterfaceInfrastructure) networkInfrastructure {
	result := networkInfrastructure{SampledAt: now.UTC(), Interfaces: interfaces}
	if !s.at.IsZero() && now.After(s.at) {
		result.IntervalSeconds = now.Sub(s.at).Seconds()
		result.Rates = make(map[string]float64)
		for name, value := range counters {
			if previous, ok := s.previous[name]; ok && value >= previous {
				result.Rates[name] = float64(value-previous) / result.IntervalSeconds
			}
		}
	}
	if s.at.IsZero() || now.After(s.at) {
		s.previous, s.at = counters, now
		s.cached = result
	}
	return result
}
