package m365

import "time"

// rawDriveItem is Graph's wire shape for /drives/{}/items. The public
// DriveItem (client.go) is a flatter projection — we materialize one
// from the other in materialize().
type rawDriveItem struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	WebURL  string    `json:"webUrl"`
	Size    int64     `json:"size"`
	LastMod time.Time `json:"lastModifiedDateTime"`
	Folder  *struct{} `json:"folder,omitempty"`
	File    *struct {
		MimeType string `json:"mimeType,omitempty"`
	} `json:"file,omitempty"`
	ParentRef *struct {
		Path string `json:"path"`
	} `json:"parentReference,omitempty"`
}

func (r rawDriveItem) materialize() DriveItem {
	d := DriveItem{
		ID:       r.ID,
		Name:     r.Name,
		WebURL:   r.WebURL,
		Size:     r.Size,
		LastMod:  r.LastMod,
		IsFolder: r.Folder != nil,
		IsFile:   r.File != nil,
	}
	if r.File != nil {
		d.MimeType = r.File.MimeType
	}
	if r.ParentRef != nil {
		d.ParentPath = r.ParentRef.Path
	}
	return d
}

// rawMessage is Graph's wire shape for /messages.
type rawMessage struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	From    *struct {
		EmailAddress struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from,omitempty"`
	Received time.Time `json:"receivedDateTime"`
	Body     *struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body,omitempty"`
	IsRead bool `json:"isRead"`
}

func (r rawMessage) materialize() Message {
	m := Message{
		ID:       r.ID,
		Subject:  r.Subject,
		Received: r.Received,
		IsRead:   r.IsRead,
	}
	if r.From != nil {
		m.FromAddr = r.From.EmailAddress.Address
	}
	if r.Body != nil {
		m.BodyType = r.Body.ContentType
		m.BodyText = r.Body.Content
	}
	return m
}
