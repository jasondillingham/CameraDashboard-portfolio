package handlers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// onedriveToken caches the OAuth2 token
var (
	odToken      string
	odTokenExpiry time.Time
	odTokenMu    sync.Mutex
)

// getGraphToken obtains (or returns cached) client credentials token
func getGraphToken(tenantID, clientID, clientSecret string) (string, error) {
	odTokenMu.Lock()
	defer odTokenMu.Unlock()

	if odToken != "" && time.Now().Before(odTokenExpiry) {
		return odToken, nil
	}

	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)

	data := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"scope":         {"https://graph.microsoft.com/.default"},
		"grant_type":    {"client_credentials"},
	}

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("token decode failed: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("token error: %s — %s", result.Error, result.ErrorDesc)
	}

	odToken = result.AccessToken
	// Expire 5 minutes early to avoid edge cases
	odTokenExpiry = time.Now().Add(time.Duration(result.ExpiresIn-300) * time.Second)

	return odToken, nil
}

// resolveUserUPN maps an AD sAMAccountName to a UPN via Graph API
func resolveUserUPN(token, username string) (string, error) {
	// If it already looks like a UPN, use it directly
	if strings.Contains(username, "@") {
		return username, nil
	}

	// Strip common suffixes like "admin" for lookup
	// Try exact match first, then without "admin" suffix
	candidates := []string{username}
	if strings.HasSuffix(username, "admin") {
		candidates = append(candidates, strings.TrimSuffix(username, "admin"))
	}

	for _, name := range candidates {
		searchURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/users?$filter=startswith(userPrincipalName,'%s')&$select=id,userPrincipalName&$top=5", url.QueryEscape(name))

		req, _ := http.NewRequest("GET", searchURL, nil)
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		var result struct {
			Value []struct {
				ID  string `json:"id"`
				UPN string `json:"userPrincipalName"`
			} `json:"value"`
		}
		json.NewDecoder(resp.Body).Decode(&result)

		if len(result.Value) > 0 {
			return result.Value[0].UPN, nil
		}
	}

	return "", fmt.Errorf("could not resolve UPN for user: %s", username)
}

// ensureOneDriveFolder creates a folder if it doesn't exist, returns the folder ID.
// Uses path-based lookup (lightweight) instead of $filter queries (which get throttled).
func ensureOneDriveFolder(token, userUPN, parentPath, folderName string) (string, error) {
	// Try direct path-based lookup first — this is a simple GET, not a filtered list
	var folderPath string
	if parentPath == "" {
		folderPath = folderName
	} else {
		folderPath = parentPath + "/" + folderName
	}

	lookupURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/drive/root:/%s",
		url.PathEscape(userUPN), url.PathEscape(folderPath))

	req, _ := http.NewRequest("GET", lookupURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("lookup folder failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		var item struct {
			ID string `json:"id"`
		}
		json.NewDecoder(resp.Body).Decode(&item)
		if item.ID != "" {
			return item.ID, nil
		}
	}

	// Folder doesn't exist — create it
	var createURL string
	if parentPath == "" {
		createURL = fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/drive/root/children",
			url.PathEscape(userUPN))
	} else {
		// Look up parent ID first
		parentURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/drive/root:/%s",
			url.PathEscape(userUPN), url.PathEscape(parentPath))
		req, _ := http.NewRequest("GET", parentURL, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		pResp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("lookup parent folder failed: %w", err)
		}
		defer pResp.Body.Close()
		var parent struct {
			ID string `json:"id"`
		}
		json.NewDecoder(pResp.Body).Decode(&parent)
		if parent.ID == "" {
			return "", fmt.Errorf("parent folder not found: %s", parentPath)
		}
		createURL = fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/drive/items/%s/children",
			url.PathEscape(userUPN), parent.ID)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"name":                              folderName,
		"folder":                            map[string]interface{}{},
		"@microsoft.graph.conflictBehavior": "fail",
	})

	req, _ = http.NewRequest("POST", createURL, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create folder failed: %w", err)
	}
	defer resp2.Body.Close()

	var createResult struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.NewDecoder(resp2.Body).Decode(&createResult)

	if createResult.Error != nil {
		if createResult.Error.Code == "nameAlreadyExists" {
			// Race condition — folder was created between our check and create. Look it up again.
			return ensureOneDriveFolder(token, userUPN, parentPath, folderName)
		}
		return "", fmt.Errorf("create folder error: %s — %s", createResult.Error.Code, createResult.Error.Message)
	}

	if createResult.ID == "" {
		return "", fmt.Errorf("create folder returned no ID")
	}

	return createResult.ID, nil
}

// uploadFileToOneDrive uploads a file using an upload session (supports large files).
// Returns the item ID of the uploaded file.
func uploadFileToOneDrive(token, userUPN, folderID, fileName, localPath string, onProgress func(pct int)) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	fileSize := stat.Size()

	// Create upload session
	sessionURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/drive/items/%s:/%s:/createUploadSession",
		url.PathEscape(userUPN), folderID, url.PathEscape(fileName))

	sessionBody, _ := json.Marshal(map[string]interface{}{
		"item": map[string]interface{}{
			"@microsoft.graph.conflictBehavior": "rename",
		},
	})

	req, _ := http.NewRequest("POST", sessionURL, bytes.NewReader(sessionBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create upload session: %w", err)
	}
	defer resp.Body.Close()

	var session struct {
		UploadURL string `json:"uploadUrl"`
		Error     *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&session)

	if session.Error != nil {
		return "", fmt.Errorf("upload session error: %s — %s", session.Error.Code, session.Error.Message)
	}
	if session.UploadURL == "" {
		return "", fmt.Errorf("upload session returned no URL")
	}

	// Upload in 10MB chunks
	const chunkSize = 10 * 1024 * 1024
	var offset int64
	buf := make([]byte, chunkSize)
	var itemID string

	for offset < fileSize {
		end := offset + chunkSize
		if end > fileSize {
			end = fileSize
		}
		n, err := f.Read(buf[:end-offset])
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read chunk at offset %d: %w", offset, err)
		}
		if n == 0 {
			break
		}

		chunk := buf[:n]
		contentRange := fmt.Sprintf("bytes %d-%d/%d", offset, offset+int64(n)-1, fileSize)

		req, _ := http.NewRequest("PUT", session.UploadURL, bytes.NewReader(chunk))
		req.Header.Set("Content-Length", fmt.Sprintf("%d", n))
		req.Header.Set("Content-Range", contentRange)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("upload chunk %s: %w", contentRange, err)
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 || resp.StatusCode == 201 {
			// Upload complete — parse item ID
			var completed struct {
				ID string `json:"id"`
			}
			json.Unmarshal(respBody, &completed)
			itemID = completed.ID
			break
		} else if resp.StatusCode == 202 {
			// Accepted — more chunks needed
		} else {
			return "", fmt.Errorf("upload chunk failed (HTTP %d): %s", resp.StatusCode, string(respBody))
		}

		offset += int64(n)
		if onProgress != nil {
			pct := int(float64(offset) / float64(fileSize) * 100)
			if pct > 99 {
				pct = 99
			}
			onProgress(pct)
		}
	}

	if onProgress != nil {
		onProgress(100)
	}

	return itemID, nil
}

// createShareLink creates an anonymous view link for a OneDrive item.
// Returns the share URL.
func createShareLink(token, userUPN, itemID string) (string, error) {
	shareURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/drive/items/%s/createLink",
		url.PathEscape(userUPN), itemID)

	body, _ := json.Marshal(map[string]interface{}{
		"type":  "view",
		"scope": "organization",
	})

	req, _ := http.NewRequest("POST", shareURL, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create share link: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Link *struct {
			WebURL string `json:"webUrl"`
		} `json:"link"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if result.Error != nil {
		return "", fmt.Errorf("share link error: %s — %s", result.Error.Code, result.Error.Message)
	}
	if result.Link == nil || result.Link.WebURL == "" {
		return "", fmt.Errorf("share link returned no URL")
	}

	return result.Link.WebURL, nil
}

// DeleteOneDriveFileByShareLink deletes a OneDrive file using its share link.
// This is used during retry to remove the old file before re-exporting.
func DeleteOneDriveFileByShareLink(tenantID, clientID, clientSecret, shareLink string) error {
	if shareLink == "" {
		return nil
	}

	token, err := getGraphToken(tenantID, clientID, clientSecret)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	// Encode the share URL to a share token for the Graph API
	// https://learn.microsoft.com/en-us/graph/api/shares-get
	encoded := base64.StdEncoding.EncodeToString([]byte(shareLink))
	encoded = "u!" + strings.TrimRight(strings.NewReplacer("/", "_", "+", "-").Replace(encoded), "=")

	// Resolve the share to get the driveItem
	resolveURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/shares/%s/driveItem", encoded)
	req, _ := http.NewRequest("GET", resolveURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("resolve share: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("resolve share returned HTTP %d", resp.StatusCode)
	}

	var item struct {
		ID            string `json:"id"`
		ParentRef     struct {
			DriveID string `json:"driveId"`
		} `json:"parentReference"`
	}
	json.NewDecoder(resp.Body).Decode(&item)

	if item.ID == "" || item.ParentRef.DriveID == "" {
		return fmt.Errorf("could not resolve share to drive item")
	}

	// Delete the item
	deleteURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/drives/%s/items/%s", item.ParentRef.DriveID, item.ID)
	delReq, _ := http.NewRequest("DELETE", deleteURL, nil)
	delReq.Header.Set("Authorization", "Bearer "+token)

	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		return fmt.Errorf("delete item: %w", err)
	}
	defer delResp.Body.Close()

	if delResp.StatusCode != 204 && delResp.StatusCode != 200 {
		return fmt.Errorf("delete returned HTTP %d", delResp.StatusCode)
	}

	return nil
}

// SendExportEmail sends an email to the user via Microsoft Graph API with the export share link.
func SendExportEmail(tenantID, clientID, clientSecret, fromAddress, userUPN, subject, cameraName, nvrName, startTime, endTime, shareLink, exportName, thumbnailPath string) error {
	token, err := getGraphToken(tenantID, clientID, clientSecret)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	if subject == "" {
		subject = "Your camera export is ready"
	}
	if exportName != "" {
		subject = subject + ": " + exportName
	}

	nameRow := ""
	if exportName != "" {
		nameRow = fmt.Sprintf(`<tr><td style="padding: 6px 12px; font-weight: bold; color: #555;">Export Name</td><td style="padding: 6px 12px;">%s</td></tr>`, exportName)
	}

	// Build thumbnail HTML — only if we have a valid thumbnail file
	thumbnailHTML := ""
	var thumbnailB64 string
	if thumbnailPath != "" {
		if thumbData, readErr := os.ReadFile(thumbnailPath); readErr == nil && len(thumbData) > 100 {
			thumbnailB64 = base64.StdEncoding.EncodeToString(thumbData)
			thumbnailHTML = `<div style="margin: 16px 0;"><img src="cid:export_thumbnail" style="max-width: 100%; width: 640px; border-radius: 8px; border: 1px solid #ddd;" alt="Export Preview"></div>`
			exportLog("Camera Export: Email thumbnail loaded (%d bytes, base64 %d chars)", len(thumbData), len(thumbnailB64))
		} else {
			exportLog("Camera Export: Email thumbnail read failed: %v (path=%s)", readErr, thumbnailPath)
		}
	} else {
		exportLog("Camera Export: No thumbnail path provided for email")
	}

	body := fmt.Sprintf(`<div style="font-family: Arial, sans-serif; max-width: 600px;">
<h2 style="color: #1a56db;">Camera Export Ready</h2>
<p>Your camera export has been processed and uploaded to OneDrive.</p>
%s<table style="border-collapse: collapse; margin: 16px 0;">
%s<tr><td style="padding: 6px 12px; font-weight: bold; color: #555;">Camera</td><td style="padding: 6px 12px;">%s</td></tr>
<tr><td style="padding: 6px 12px; font-weight: bold; color: #555;">Location</td><td style="padding: 6px 12px;">%s</td></tr>
<tr><td style="padding: 6px 12px; font-weight: bold; color: #555;">Time Range</td><td style="padding: 6px 12px;">%s to %s</td></tr>
</table>
<p><a href="%s" style="display: inline-block; padding: 10px 24px; background: #1a56db; color: #fff; text-decoration: none; border-radius: 6px; font-weight: bold;">View Export in OneDrive</a></p>
<p style="color: #888; font-size: 0.85em; margin-top: 24px;">This file is also available in your OneDrive under CMSnet Camera Exports.</p>
</div>`, thumbnailHTML, nameRow, cameraName, nvrName, startTime, endTime, shareLink)

	message := map[string]interface{}{
		"subject": subject,
		"body": map[string]interface{}{
			"contentType": "HTML",
			"content":     body,
		},
		"toRecipients": []map[string]interface{}{
			{
				"emailAddress": map[string]interface{}{
					"address": userUPN,
				},
			},
		},
	}

	// Add inline attachment if thumbnail is available
	if thumbnailB64 != "" {
		message["attachments"] = []map[string]interface{}{
			{
				"@odata.type":  "#microsoft.graph.fileAttachment",
				"name":         "export_preview.jpg",
				"contentType":  "image/jpeg",
				"contentBytes": thumbnailB64,
				"contentId":    "export_thumbnail",
				"isInline":     true,
			},
		}
	}

	msg := map[string]interface{}{
		"message":         message,
		"saveToSentItems": false,
	}

	msgJSON, _ := json.Marshal(msg)

	sendURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/sendMail",
		url.PathEscape(fromAddress))

	req, _ := http.NewRequest("POST", sendURL, bytes.NewReader(msgJSON))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send mail request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 202 || resp.StatusCode == 200 {
		hasThumb := "without thumbnail"
		if thumbnailB64 != "" {
			hasThumb = "with thumbnail"
		}
		exportLog("Camera Export: Email sent successfully %s (HTTP %d, payload %d bytes)", hasThumb, resp.StatusCode, len(msgJSON))
		return nil
	}

	var errResult struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&errResult)
	if errResult.Error != nil {
		return fmt.Errorf("send mail error (HTTP %d): %s — %s", resp.StatusCode, errResult.Error.Code, errResult.Error.Message)
	}

	return fmt.Errorf("send mail failed (HTTP %d)", resp.StatusCode)
}

// UploadExportToOneDrive handles the full OneDrive upload flow for an export job:
// 1. Get token
// 2. Resolve user UPN
// 3. Ensure "CMSnet Camera Exports" folder
// 4. Ensure date subfolder (MM-DD-YYYY)
// 5. Upload file
// 6. Create share link
func UploadExportToOneDrive(tenantID, clientID, clientSecret, folderName, username, filePath, fileName, jobUID string, onProgress func(pct int)) (string, error) {
	if folderName == "" {
		folderName = "CMSnet Camera Exports"
	}

	// Step 1: Get token
	token, err := getGraphToken(tenantID, clientID, clientSecret)
	if err != nil {
		return "", fmt.Errorf("auth: %w", err)
	}

	// Step 2: Resolve user UPN
	upn, err := resolveUserUPN(token, username)
	if err != nil {
		return "", fmt.Errorf("resolve user: %w", err)
	}
	exportLog("Camera Export: OneDrive resolved %s → %s", username, upn)

	// Step 3: Ensure root folder
	rootFolderID, err := ensureOneDriveFolder(token, upn, "", folderName)
	if err != nil {
		return "", fmt.Errorf("ensure root folder: %w", err)
	}
	exportLog("Camera Export: OneDrive root folder '%s' ID=%s", folderName, rootFolderID)

	// Step 4: Ensure date subfolder (MM-DD-YYYY)
	dateFolder := time.Now().Format("01-02-2006")
	dateFolderID, err := ensureOneDriveFolder(token, upn, folderName, dateFolder)
	if err != nil {
		return "", fmt.Errorf("ensure date folder: %w", err)
	}
	exportLog("Camera Export: OneDrive date folder '%s' ID=%s", dateFolder, dateFolderID)

	// Step 5: Upload file
	exportLog("Camera Export: OneDrive uploading %s to %s/%s/%s", jobUID, folderName, dateFolder, fileName)
	itemID, err := uploadFileToOneDrive(token, upn, dateFolderID, fileName, filePath, onProgress)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	exportLog("Camera Export: OneDrive upload complete for %s (itemID=%s)", jobUID, itemID)

	// Step 6: Create share link
	shareLink, err := createShareLink(token, upn, itemID)
	if err != nil {
		exportLog("Camera Export: OneDrive share link failed for %s: %v", jobUID, err)
		return "", fmt.Errorf("share link: %w", err)
	}
	exportLog("Camera Export: OneDrive share link for %s: %s", jobUID, shareLink)

	return shareLink, nil
}
