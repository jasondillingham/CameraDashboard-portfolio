package models

import (
	"strings"
	"time"
)

// AccessLog represents a single page view event
type AccessLog struct {
	ID         int64     `json:"id"`
	UserID     string    `json:"userId"`
	UserName   string    `json:"userName"`
	Path       string    `json:"path"`
	Method     string    `json:"method"`
	StatusCode int       `json:"statusCode"`
	ClientIP   string    `json:"clientIp"`
	UserAgent  string    `json:"userAgent"`
	Timestamp  time.Time `json:"timestamp"`
}

// ActiveSession represents a currently online user
type ActiveSession struct {
	ID           int64     `json:"id"`
	SessionToken string    `json:"sessionToken"`
	UserID       string    `json:"userId"`
	UserName     string    `json:"userName"`
	Email        string    `json:"email"`
	ClientIP     string    `json:"clientIp"`
	UserAgent    string    `json:"userAgent"`
	CurrentPage  string    `json:"currentPage"`
	LastActivity time.Time `json:"lastActivity"`
	CreatedAt    time.Time `json:"createdAt"`
}

// DeviceInfo returns a simplified device description from user agent
func (s *ActiveSession) DeviceInfo() string {
	ua := s.UserAgent
	if len(ua) == 0 {
		return "Unknown"
	}

	// Simple device detection
	switch {
	case strings.Contains(ua, "iPhone"):
		return "iPhone"
	case strings.Contains(ua, "iPad"):
		return "iPad"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Mac OS"):
		if strings.Contains(ua, "Chrome") {
			return "Mac/Chrome"
		} else if strings.Contains(ua, "Safari") {
			return "Mac/Safari"
		} else if strings.Contains(ua, "Firefox") {
			return "Mac/Firefox"
		}
		return "Mac"
	case strings.Contains(ua, "Windows"):
		if strings.Contains(ua, "Chrome") {
			return "Win/Chrome"
		} else if strings.Contains(ua, "Firefox") {
			return "Win/Firefox"
		} else if strings.Contains(ua, "Edge") {
			return "Win/Edge"
		}
		return "Windows"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	default:
		return "Unknown"
	}
}


// AccessStats holds aggregated access statistics
type AccessStats struct {
	OnlineNow       int                 `json:"onlineNow"`
	TodayViews      int                 `json:"todayViews"`
	TodayUniqueUsers int                `json:"todayUniqueUsers"`
	TopDashboards   []DashboardViewCount `json:"topDashboards"`
	TopUsers        []UserViewCount      `json:"topUsers"`
}

// DashboardViewCount represents view count for a dashboard path
type DashboardViewCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

// UserViewCount represents view count for a user
type UserViewCount struct {
	UserID   string `json:"userId"`
	UserName string `json:"userName"`
	Count    int    `json:"count"`
}

// UniqueUserDetail represents a unique user with activity details for today
type UniqueUserDetail struct {
	UserID    string    `json:"userId"`
	UserName  string    `json:"userName"`
	PageViews int       `json:"pageViews"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

// AdminDashboardData holds all data for the admin dashboard template
type AdminDashboardData struct {
	BaseDashboardData
	Tab            string           `json:"tab"`
	LockoutMode    bool             `json:"lockoutMode"`
	Sessions       []ActiveSession  `json:"sessions"`
	Logs           []AccessLog      `json:"logs"`
	Stats          *AccessStats     `json:"stats"`
	User           *UserInfo        `json:"user"`
	IsAdmin        bool             `json:"isAdmin"`
	UpdatedAt      time.Time        `json:"updatedAt"`

	// Filters for logs
	FilterUser     string           `json:"filterUser"`
	FilterPath     string           `json:"filterPath"`
	FilterDateFrom string           `json:"filterDateFrom"`
	FilterDateTo   string           `json:"filterDateTo"`
	Page           int              `json:"page"`
	PageSize       int              `json:"pageSize"`
	TotalLogs      int              `json:"totalLogs"`
	TotalPages     int              `json:"totalPages"`

	// Quick filter date helpers
	Today     string `json:"today"`
	Week7Ago  string `json:"week7Ago"`
	Days30Ago string `json:"days30Ago"`

	// User dropdown for logs filter
	LogUsers []string `json:"logUsers,omitempty"`
}
