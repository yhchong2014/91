package webdav

import (
	"encoding/xml"
	"io"
	"path"
	"strconv"
	"strings"
	"time"
)

// davResponse represents one <d:response> element from a PROPFIND multistatus.
type davResponse struct {
	Name        string
	Size        int64
	IsDir       bool
	ContentType string
	ModTime     time.Time
}

// multiStatus is the top-level <d:multistatus> XML element.
type multiStatus struct {
	XMLName   xml.Name   `xml:"DAV: multistatus"`
	Responses []response `xml:"response"`
}

type response struct {
	Href     string     `xml:"href"`
	PropStat []propStat `xml:"propstat"`
}

type propStat struct {
	Prop   prop   `xml:"prop"`
	Status string `xml:"status"`
}

type prop struct {
	DisplayName      string       `xml:"displayname"`
	GetContentLength string       `xml:"getcontentlength"`
	GetLastModified  string       `xml:"getlastmodified"`
	GetContentType   string       `xml:"getcontenttype"`
	ResourceType     resourceType `xml:"resourcetype"`
}

type resourceType struct {
	Collection *struct{} `xml:"collection"`
}

func parseMultiStatus(r io.Reader) ([]davResponse, error) {
	var ms multiStatus
	if err := xml.NewDecoder(r).Decode(&ms); err != nil {
		return nil, err
	}

	var results []davResponse
	for _, resp := range ms.Responses {
		name := path.Base(strings.TrimRight(resp.Href, "/"))
		if name == "" || name == "." {
			name = "/"
		}

		// Use the first propstat that has properties.
		var p prop
		for _, ps := range resp.PropStat {
			p = ps.Prop
			break
		}

		isDir := p.ResourceType.Collection != nil

		var size int64
		if s := strings.TrimSpace(p.GetContentLength); s != "" {
			size, _ = strconv.ParseInt(s, 10, 64)
		}

		var modTime time.Time
		if s := strings.TrimSpace(p.GetLastModified); s != "" {
			// WebDAV uses RFC 1123 format.
			modTime, _ = time.Parse(time.RFC1123, s)
			if modTime.IsZero() {
				modTime, _ = time.Parse(time.RFC1123Z, s)
			}
		}

		displayName := strings.TrimSpace(p.DisplayName)
		if displayName != "" {
			name = displayName
		}

		results = append(results, davResponse{
			Name:        name,
			Size:        size,
			IsDir:       isDir,
			ContentType: p.GetContentType,
			ModTime:     modTime,
		})
	}
	return results, nil
}
