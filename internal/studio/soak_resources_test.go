package studio

// CPU is cumulative for this process instance. The working set and number of
// open handles are instantaneous observations at the sampling boundary.
type soakProcessResources256 struct {
	CPUMS         uint64 `json:"cpu_ms"`
	ResidentBytes uint64 `json:"resident_bytes"`
	HandleCount   uint32 `json:"handle_count"`
}

type soakResourcePair256 struct {
	Service soakProcessResources256 `json:"service"`
	Driver  soakProcessResources256 `json:"driver"`
}
