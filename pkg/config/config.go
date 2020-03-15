package config

type DaemonConfig struct {
	Plugin         string `json:"plugin"`          // Subnet manager plugin name
	PeriodicUpdate int    `json:"periodic_update"` // Interval between every check for the added and deleted pods
	GuidPool       GuidPoolConfig
	SubnetManager  SubnetManagerPluginConfig
}

type GuidPoolConfig struct {
	GuidRangeStart string `json:"guid_range_start"` // First guid of the pool
	GuidRangeEnd   string `json:"guid_range_end"`   // Last of the guid pool
}

type SubnetManagerPluginConfig struct {
	UFMConfig
}

type UFMConfig struct {
	Username    string `json:"ufm_username"`    // Username of ufm
	Password    string `json:"ufm_password"`    // Password of ufm
	Address     string `json:"ufm_address"`     // IP address or hostname of ufm servere
	Port        int    `json:"ufm_port"`        // REST API port of ufm
	HttpSchema  string `json:"ufm_httpSchema"`  // http or https
	Certificate string `json:"ufm_certificate"` // Certificate of ufm
}
