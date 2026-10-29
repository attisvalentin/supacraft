package tenant

// StorageConfig holds the file storage driver and its credentials.
// Part of TenantConfig.
type StorageConfig struct {
	Driver       string         `json:"driver"`
	Config       map[string]any `json:"config,omitempty"`
	MaxFileSize  int64          `json:"max_file_size,omitempty"`
	AllowedMimes []string       `json:"allowed_mimes,omitempty"`
}

// FileEndpointSettings controls enabled/auth for a single file endpoint.
type FileEndpointSettings struct {
	Enabled *bool `json:"enabled,omitempty"`
	Auth    *bool `json:"auth,omitempty"`
}

func (f FileEndpointSettings) IsEnabled() bool {
	return f.Enabled == nil || *f.Enabled
}

func (f FileEndpointSettings) AuthRequired() bool {
	return f.Auth != nil && *f.Auth
}

// FileEndpoints controls per-route toggles for file operations.
type FileEndpoints struct {
	Upload   FileEndpointSettings `json:"upload,omitempty"`
	Download FileEndpointSettings `json:"download,omitempty"`
	Delete   FileEndpointSettings `json:"delete,omitempty"`
}
