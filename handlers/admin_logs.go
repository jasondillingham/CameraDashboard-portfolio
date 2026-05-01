package handlers

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cameradashboard/db"
	"cameradashboard/models"
)

// AdminLogsHandler serves the access logs tab content
func AdminLogsHandler(w http.ResponseWriter, r *http.Request) {
	// Parse filters
	userFilter := r.URL.Query().Get("user")
	pathFilter := r.URL.Query().Get("path")
	dateFromStr := r.URL.Query().Get("from")
	dateToStr := r.URL.Query().Get("to")
	pageStr := r.URL.Query().Get("page")
	pageSizeStr := r.URL.Query().Get("pageSize")

	// Default to today if no date filters provided
	today := time.Now().Format("2006-01-02")
	if dateFromStr == "" && dateToStr == "" {
		dateFromStr = today
		dateToStr = today
	}

	var dateFrom, dateTo *time.Time
	if dateFromStr != "" {
		if t, err := time.Parse("2006-01-02", dateFromStr); err == nil {
			dateFrom = &t
		}
	}
	if dateToStr != "" {
		if t, err := time.Parse("2006-01-02", dateToStr); err == nil {
			// Set to end of day
			endOfDay := t.Add(24*time.Hour - time.Second)
			dateTo = &endOfDay
		}
	}

	page := 1
	if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
		page = p
	}

	pageSize := 50
	if ps, err := strconv.Atoi(pageSizeStr); err == nil && ps > 0 && ps <= 200 {
		pageSize = ps
	}

	logs, total, err := db.GetAccessLogs(userFilter, pathFilter, dateFrom, dateTo, page, pageSize)
	if err != nil {
		log.Printf("Error getting access logs: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	totalPages := (total + pageSize - 1) / pageSize

	// Calculate quick filter dates
	now := time.Now()
	todayStr := now.Format("2006-01-02")
	week7AgoStr := now.AddDate(0, 0, -6).Format("2006-01-02")
	days30AgoStr := now.AddDate(0, 0, -29).Format("2006-01-02")

	// Get distinct users for filter dropdown
	logUsers, err := db.GetDistinctLogUsers()
	if err != nil {
		log.Printf("Error getting distinct log users: %v", err)
	}

	data := &models.AdminDashboardData{
		Tab:            "logs",
		Logs:           logs,
		FilterUser:     userFilter,
		FilterPath:     pathFilter,
		FilterDateFrom: dateFromStr,
		FilterDateTo:   dateToStr,
		Page:           page,
		PageSize:       pageSize,
		TotalLogs:      total,
		TotalPages:     totalPages,
		UpdatedAt:      time.Now(),
		Today:          todayStr,
		Week7Ago:       week7AgoStr,
		Days30Ago:      days30AgoStr,
		LogUsers:       logUsers,
	}

	renderTemplate(w, "admin_logs_content.html", data)
}

// AdminNotesHandler serves the project notes tab from notes.md
func AdminNotesHandler(w http.ResponseWriter, r *http.Request) {
	mdBytes, err := os.ReadFile("notes.md")
	if err != nil {
		log.Printf("Error reading notes.md: %v", err)
		mdBytes = []byte("# Notes\n\nNo notes.md file found.")
	}

	html := markdownToHTML(string(mdBytes))

	data := map[string]interface{}{
		"NotesHTML": template.HTML(html),
	}

	renderTemplate(w, "admin_notes_content.html", data)
}

// markdownToHTML converts basic markdown to HTML (headers, lists, bold, code, paragraphs)
func markdownToHTML(md string) string {
	var out strings.Builder
	lines := strings.Split(md, "\n")
	inList := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Close list if we leave a list context
		if inList && !strings.HasPrefix(trimmed, "- ") {
			out.WriteString("</ul>\n")
			inList = false
		}

		if trimmed == "" {
			continue
		}

		// Headers
		if strings.HasPrefix(trimmed, "### ") {
			out.WriteString("<h3>" + inlineMarkdown(trimmed[4:]) + "</h3>\n")
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			out.WriteString("<h2>" + inlineMarkdown(trimmed[3:]) + "</h2>\n")
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			out.WriteString("<h1>" + inlineMarkdown(trimmed[2:]) + "</h1>\n")
			continue
		}

		// List items
		if strings.HasPrefix(trimmed, "- ") {
			if !inList {
				out.WriteString("<ul>\n")
				inList = true
			}
			out.WriteString("<li>" + inlineMarkdown(trimmed[2:]) + "</li>\n")
			continue
		}

		// Numbered list items
		if len(trimmed) > 2 && trimmed[0] >= '1' && trimmed[0] <= '9' && strings.Contains(trimmed[:3], ". ") {
			idx := strings.Index(trimmed, ". ")
			out.WriteString("<p>" + inlineMarkdown(trimmed[idx+2:]) + "</p>\n")
			continue
		}

		// Regular paragraph
		out.WriteString("<p>" + inlineMarkdown(trimmed) + "</p>\n")
	}

	if inList {
		out.WriteString("</ul>\n")
	}

	return out.String()
}

// inlineMarkdown handles bold (**text**) and code (`text`) within a line
func inlineMarkdown(s string) string {
	// Escape HTML first
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")

	// Process backtick code spans
	var result strings.Builder
	for {
		start := strings.Index(s, "`")
		if start == -1 {
			break
		}
		end := strings.Index(s[start+1:], "`")
		if end == -1 {
			break
		}
		result.WriteString(s[:start])
		result.WriteString("<code>" + s[start+1:start+1+end] + "</code>")
		s = s[start+1+end+1:]
	}
	result.WriteString(s)
	s = result.String()

	// Process bold **text**
	var result2 strings.Builder
	for {
		start := strings.Index(s, "**")
		if start == -1 {
			break
		}
		end := strings.Index(s[start+2:], "**")
		if end == -1 {
			break
		}
		result2.WriteString(s[:start])
		result2.WriteString("<strong>" + s[start+2:start+2+end] + "</strong>")
		s = s[start+2+end+2:]
	}
	result2.WriteString(s)

	return result2.String()
}

// AdminDocsHandler serves the architecture documentation tab
func AdminDocsHandler(w http.ResponseWriter, r *http.Request) {
	nvrs := GetCameraNVRs()
	totalChannels := 0
	for _, nvr := range nvrs {
		totalChannels += nvr.Channels
	}

	data := map[string]interface{}{
		"Go2RTCURL":       go2rtcBaseURL,
		"NVRCount":        len(nvrs),
		"TotalChannels":   totalChannels,
		"FFmpegAvailable": ffmpegAvailable,
	}

	renderTemplate(w, "admin_docs_content.html", data)
}

// AdminClearLogsHandler clears all access logs
func AdminClearLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	adminID := getAuthUserID(r)

	count, err := db.ClearAccessLogs()
	if err != nil {
		log.Printf("Error clearing access logs: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	log.Printf("Admin %s cleared %d access logs", adminID, count)

	// Return updated logs tab
	AdminLogsHandler(w, r)
}
