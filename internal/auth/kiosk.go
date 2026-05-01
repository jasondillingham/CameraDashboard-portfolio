package auth

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
)

// KioskConfig represents a single kiosk device configuration
type KioskConfig struct {
	CN                string   `json:"cn"`                // Certificate Common Name (must match cert)
	DisplayName       string   `json:"display_name"`      // Human-readable name for logs/display
	Location          string   `json:"location"`          // Physical location description
	DefaultDashboard  string   `json:"default_dashboard"` // Default dashboard path to redirect to
	AllowedDashboards []string `json:"allowed_dashboards"` // List of allowed dashboard paths
	Enabled           bool     `json:"enabled"`           // Whether this kiosk is active
}

// KioskRegistry holds the kiosk configuration file structure
type KioskRegistry struct {
	Kiosks []KioskConfig `json:"kiosks"`
}

// KioskManager manages kiosk authentication via client certificates
type KioskManager struct {
	mu             sync.RWMutex
	kiosks         map[string]*KioskConfig // CN -> KioskConfig
	trustedProxies map[string]bool         // Set of trusted proxy IPs
	configPath     string
}

// NewKioskManager creates a new KioskManager and loads the kiosk registry
func NewKioskManager(kiosksFile string, trustedProxies []string) (*KioskManager, error) {
	km := &KioskManager{
		kiosks:         make(map[string]*KioskConfig),
		trustedProxies: make(map[string]bool),
		configPath:     kiosksFile,
	}

	// Build trusted proxies set
	for _, ip := range trustedProxies {
		km.trustedProxies[ip] = true
	}

	// Load initial kiosk configuration
	if err := km.Reload(); err != nil {
		return nil, err
	}

	return km, nil
}

// Reload reloads the kiosk registry from the config file
func (km *KioskManager) Reload() error {
	km.mu.Lock()
	defer km.mu.Unlock()

	data, err := os.ReadFile(km.configPath)
	if err != nil {
		return fmt.Errorf("failed to read kiosks file: %w", err)
	}

	var registry KioskRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		return fmt.Errorf("failed to parse kiosks file: %w", err)
	}

	// Rebuild the kiosks map
	km.kiosks = make(map[string]*KioskConfig)
	for i := range registry.Kiosks {
		kiosk := &registry.Kiosks[i]
		km.kiosks[kiosk.CN] = kiosk
	}

	log.Printf("[INFO] Loaded %d kiosk configurations from %s", len(km.kiosks), km.configPath)
	return nil
}

// IsTrustedProxy checks if the given IP is a trusted proxy
func (km *KioskManager) IsTrustedProxy(ip string) bool {
	km.mu.RLock()
	defer km.mu.RUnlock()
	return km.trustedProxies[ip]
}

// ValidateCN checks if a certificate CN is registered and enabled
func (km *KioskManager) ValidateCN(cn string) (*KioskConfig, error) {
	km.mu.RLock()
	defer km.mu.RUnlock()

	kiosk, exists := km.kiosks[cn]
	if !exists {
		return nil, fmt.Errorf("unknown kiosk CN: %s", cn)
	}

	if !kiosk.Enabled {
		return nil, fmt.Errorf("kiosk %s is disabled", cn)
	}

	return kiosk, nil
}

// IsPathAllowed checks if a kiosk is allowed to access a specific dashboard path
func (km *KioskManager) IsPathAllowed(cn, path string) bool {
	km.mu.RLock()
	defer km.mu.RUnlock()

	kiosk, exists := km.kiosks[cn]
	if !exists || !kiosk.Enabled {
		return false
	}

	// Check if path is in allowed dashboards
	for _, allowed := range kiosk.AllowedDashboards {
		if allowed == path || allowed == "/" {
			return true
		}
	}

	return false
}

// KioskUser creates a User object for an authenticated kiosk
func KioskUser(kiosk *KioskConfig) *User {
	return &User{
		SamAccountName:    "kiosk:" + kiosk.CN,
		DisplayName:       kiosk.DisplayName,
		Description:       fmt.Sprintf("Kiosk at %s", kiosk.Location),
		DistinguishedName: "CN=" + kiosk.CN + ",OU=Kiosks,DC=local",
		Enabled:           true,
		MemberOf:          []string{"Kiosks"},
	}
}

// IsKioskUser checks if a user is a kiosk user (authenticated via certificate)
func IsKioskUser(user *User) bool {
	if user == nil {
		return false
	}
	return len(user.SamAccountName) > 6 && user.SamAccountName[:6] == "kiosk:"
}

// GetKioskCN extracts the kiosk CN from a kiosk user's SamAccountName
func GetKioskCN(user *User) string {
	if !IsKioskUser(user) {
		return ""
	}
	return user.SamAccountName[6:]
}
