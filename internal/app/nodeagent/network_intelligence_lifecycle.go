package nodeagent

// Monitoring is absent by default. Its lifecycle barrier joins old capture
// before a kernel ownership mutation, while preserving the runtime result.
func (s *Server) withNetworkIntelligenceMutation(interfaceName string, mutate func() error) error {
	if s.networkIntelligenceLifecycle == nil {
		return mutate()
	}
	return s.networkIntelligenceLifecycle.Mutate(interfaceName, mutate)
}
