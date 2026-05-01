package models

// DashboardType represents a type of dashboard (IT, Passwords, Tickets, etc.)
type DashboardType struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Slug              string `json:"slug"`
	Description       string `json:"description"`
	Icon              string `json:"icon"`              // CSS class or emoji for display
	UnderConstruction bool   `json:"underConstruction"` // Hide from main grid, show in under construction section
	Category          string `json:"category"`          // Group dashboards under a parent category
	HideFromLanding   bool   `json:"hideFromLanding"`   // Hide from landing page (accessible via other dashboards)
}

// Branch represents a branch/location
type Branch struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"` // URL-friendly name (lowercase, no spaces)
}

// Predefined dashboard types — Camera dashboards only
var DashboardTypes = []DashboardType{
	{ID: "cameras", Name: "Cameras", Slug: "cameras", Description: "NVR camera live feeds", Icon: "video"},
}

// GetDashboardTypeBySlug returns a dashboard type by its URL slug
func GetDashboardTypeBySlug(slug string) *DashboardType {
	for _, dt := range DashboardTypes {
		if dt.Slug == slug {
			return &dt
		}
	}
	return nil
}

// UserInfo holds authenticated user information for templates
type UserInfo struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

// DatabaseInfo holds information about the connected database for display
type DatabaseInfo struct {
	Server   string `json:"server"`   // Server hostname
	Database string `json:"database"` // Database name
	IsLive   bool   `json:"isLive"`   // True if connected to production (P21), false for test (P21Play)
}

// BaseDashboardData contains fields common to all dashboard data types.
// Embed this struct in dashboard-specific data types to reduce duplication.
type BaseDashboardData struct {
	DashboardType  *DashboardType  `json:"dashboardType"`
	DashboardTypes []DashboardType `json:"dashboardTypes"`
	DatabaseInfo   *DatabaseInfo   `json:"databaseInfo"`
	User           *UserInfo       `json:"user,omitempty"`
	IsAdmin        bool            `json:"isAdmin"`
	ShowDollars    bool            `json:"showDollars"`
	CSRFToken      string          `json:"-"`
	Tab            string          `json:"tab,omitempty"`
}

// SetDashboardInfo sets the common dashboard fields
func (b *BaseDashboardData) SetDashboardInfo(slug string, dbInfo *DatabaseInfo) {
	b.DashboardType = GetDashboardTypeBySlug(slug)
	b.DashboardTypes = DashboardTypes
	b.DatabaseInfo = dbInfo
}

// SetUserInfo sets the user info and admin status
func (b *BaseDashboardData) SetUserInfo(user *UserInfo, isAdmin bool) {
	b.User = user
	b.IsAdmin = isAdmin
}

// Config holds the application configuration
type Config struct {
	Database struct {
		Server   string `json:"server"`
		User     string `json:"user"`
		Password string `json:"password"`
		Database string `json:"database"`
		Options  struct {
			Encrypt                bool `json:"encrypt"`
			TrustServerCertificate bool `json:"trustServerCertificate"`
		} `json:"options"`
	} `json:"database"`
	Server struct {
		Port            int    `json:"port"`
		RefreshInterval int    `json:"refreshInterval"` // seconds
		SimulatedDate   string `json:"simulatedDate"`   // optional: use this date instead of today (format: 2006-01-02)
	} `json:"server"`
	LDAP struct {
		Host     string `json:"host"`
		BaseDN   string `json:"baseDN"`
		BindDN   string `json:"bindDN"`
		BindPass string `json:"bindPass"`
		GroupsDN string `json:"groupsDN"`
	} `json:"ldap"`
	Auth struct {
		SessionIdleTimeout string          `json:"sessionIdleTimeout"` // e.g., "24h"
		SessionLifetime    string          `json:"sessionLifetime"`    // e.g., "168h" (7 days)
		AllowedUsers       []string        `json:"allowedUsers"`       // whitelist of allowed usernames
		AdminUsers         []string        `json:"adminUsers"`         // users with admin dashboard access
		CertAuth           *CertAuthConfig `json:"certAuth,omitempty"` // client certificate authentication for kiosks
	} `json:"auth"`
	Export struct {
		ExportDir      string `json:"exportDir"`      // Local/NAS path for downloads (default: os temp dir)
		MaxConcurrent  int    `json:"maxConcurrent"`  // Max simultaneous downloads (default: 2)
		RetentionHours int    `json:"retentionHours"` // Hours to keep completed exports (default: 24)
		EncodeH264     bool   `json:"encodeH264"`     // Re-encode H.265 → H.264 after download
		OneDrive *struct {
			TenantID     string `json:"tenantId"`
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
			FolderName   string `json:"folderName"` // Root folder in user's OneDrive (default: "CMSnet Camera Exports")
		} `json:"onedrive,omitempty"`
		Email *struct {
			From    string `json:"from"`    // Sender address (e.g. "cameradashboard@example.com")
			Subject string `json:"subject"` // Subject template (default: "Your camera export is ready")
		} `json:"email,omitempty"`
	} `json:"export"`
}

// CertAuthConfig holds configuration for client certificate authentication
type CertAuthConfig struct {
	Enabled        bool     `json:"enabled"`        // Enable certificate-based authentication
	KiosksFile     string   `json:"kiosksFile"`     // Path to kiosks.json registry file
	TrustedProxies []string `json:"trustedProxies"` // IPs allowed to send X-SSL-* headers (e.g., ["127.0.0.1", "::1"])
}
