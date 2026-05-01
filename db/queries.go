package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"cameradashboard/models"

	_ "github.com/denisenkom/go-mssqldb"
)

// QueryTimeout is the maximum time allowed for a single database query.
// This prevents runaway queries from impacting production database performance.
const QueryTimeout = 5 * time.Second

// queryContext creates a context with the standard query timeout.
// Use this for all database queries to ensure they don't run indefinitely.
func queryContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), QueryTimeout)
}

var (
	db            *sql.DB
	simulatedDate time.Time
	branches      []models.Branch
	databaseInfo  *models.DatabaseInfo
)

// Predefined branches in display order
var branchList = []struct {
	ID   string
	Name string
}{
	{"10", "Hopkinsville"},
	{"20", "Russellville"},
	{"30", "Owensboro"},
	{"15", "Mayfield"},
	{"19", "Central City"},
	{"40", "Madisonville"},
	{"18", "Cadiz"},
}

// Connect establishes a connection to the database
func Connect(config *models.Config) error {
	connString := fmt.Sprintf(
		"server=%s;user id=%s;password=%s;database=%s;encrypt=%t;TrustServerCertificate=true",
		config.Database.Server,
		config.Database.User,
		config.Database.Password,
		config.Database.Database,
		config.Database.Options.Encrypt,
	)

	var err error
	db, err = sql.Open("sqlserver", connString)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	// Set simulated date if configured
	if config.Server.SimulatedDate != "" {
		parsed, err := time.Parse("2006-01-02", config.Server.SimulatedDate)
		if err != nil {
			log.Printf("Warning: invalid simulatedDate format, using real date: %v", err)
		} else {
			simulatedDate = parsed
			log.Printf("Using simulated date: %s", simulatedDate.Format("2006-01-02"))
		}
	}

	// Initialize branches list
	initBranches()

	// Store database info for display
	isLive := strings.ToUpper(config.Database.Database) == "P21"
	databaseInfo = &models.DatabaseInfo{
		Server:   config.Database.Server,
		Database: config.Database.Database,
		IsLive:   isLive,
	}

	log.Printf("Connected to database: %s", config.Database.Server)
	return nil
}

// initBranches initializes the branches list in display order
func initBranches() {
	branches = []models.Branch{}
	for _, b := range branchList {
		branches = append(branches, models.Branch{
			ID:   b.ID,
			Name: b.Name,
			Slug: strings.ToLower(strings.ReplaceAll(b.Name, " ", "-")),
		})
	}
}

// GetBranches returns all available branches
func GetBranches() []models.Branch {
	return branches
}

// GetDatabaseInfo returns information about the connected database
func GetDatabaseInfo() *models.DatabaseInfo {
	return databaseInfo
}

// GetBranchBySlug returns a branch by its URL slug
func GetBranchBySlug(slug string) *models.Branch {
	slug = strings.ToLower(slug)
	for _, b := range branches {
		if b.Slug == slug {
			return &b
		}
	}
	return nil
}

// GetBranchNameByID returns the branch name for a given branch ID
func GetBranchNameByID(id string) string {
	for _, b := range branchList {
		if b.ID == id {
			return b.Name
		}
	}
	return ""
}

// Close closes the database connection
func Close() {
	if db != nil {
		db.Close()
	}
}

// GetDB returns the underlying database connection for use by session stores
func GetDB() *sql.DB {
	return db
}

// InitSessionsTable ensures the sessions table exists in SQLite
func InitSessionsTable() error {
	if sqliteDB == nil {
		return fmt.Errorf("SQLite database not connected")
	}
	// Table creation handled by InitSQLiteTables
	log.Printf("Sessions table initialized")
	return nil
}

// getTargetDate returns the date to query for (simulated or real)
func getTargetDate() time.Time {
	if !simulatedDate.IsZero() {
		return simulatedDate
	}
	return time.Now()
}
