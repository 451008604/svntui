package svn

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type infoDocument struct {
	Entries []infoEntry `xml:"entry"`
}

type infoEntry struct {
	Kind       string `xml:"kind,attr"`
	Path       string `xml:"path,attr"`
	Revision   string `xml:"revision,attr"`
	URL        string `xml:"url"`
	Relative   string `xml:"relative-url"`
	Repository struct {
		Root string `xml:"root"`
		UUID string `xml:"uuid"`
	} `xml:"repository"`
	WC struct {
		Root  string `xml:"wcroot-abspath"`
		Depth string `xml:"depth"`
	} `xml:"wc-info"`
	Conflict struct {
		Operation string `xml:"operation,attr"`
		Type      string `xml:"type,attr"`
		Old       string `xml:"prev-base-file"`
		New       string `xml:"prev-wc-file"`
		Working   string `xml:"cur-base-file"`
		Versions  []struct {
			Side       string `xml:"side,attr"`
			Kind       string `xml:"kind,attr"`
			Path       string `xml:"path-in-repos,attr"`
			Revision   int64  `xml:"revision,attr"`
			Repository string `xml:"repos-url,attr"`
		} `xml:"version"`
	} `xml:"conflict"`
}

func parseInfo(data []byte) (Info, error) {
	var doc infoDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return Info{}, fmt.Errorf("解析 svn info: %w", err)
	}
	if len(doc.Entries) == 0 {
		return Info{}, fmt.Errorf("svn info 未返回工作副本信息")
	}
	entry := doc.Entries[0]
	revision, err := parseOptionalRevision(entry.Revision)
	if err != nil {
		return Info{}, fmt.Errorf("解析 svn info revision: %w", err)
	}
	info := Info{
		Path:              entry.Path,
		Kind:              entry.Kind,
		Revision:          revision,
		URL:               entry.URL,
		RootURL:           entry.Repository.Root,
		UUID:              entry.Repository.UUID,
		WCAbsolute:        entry.WC.Root,
		WCRelative:        entry.Relative,
		Depth:             entry.WC.Depth,
		ConflictOld:       entry.Conflict.Old,
		ConflictNew:       entry.Conflict.New,
		ConflictWorking:   entry.Conflict.Working,
		ConflictType:      entry.Conflict.Type,
		ConflictOperation: entry.Conflict.Operation,
	}
	for _, version := range entry.Conflict.Versions {
		parsed := ConflictVersion{Side: version.Side, Kind: version.Kind, Path: version.Path, Revision: version.Revision, Repository: version.Repository}
		if version.Side == "source-left" {
			info.ConflictLeft = parsed
		} else if version.Side == "source-right" {
			info.ConflictRight = parsed
		}
	}
	return info, nil
}

type statusDocument struct {
	Targets     []statusTarget     `xml:"target"`
	Changelists []statusChangelist `xml:"changelist"`
}

type statusTarget struct {
	Entries     []statusXMLItem    `xml:"entry"`
	Changelists []statusChangelist `xml:"changelist"`
}

type statusChangelist struct {
	Name    string          `xml:"name,attr"`
	Entries []statusXMLItem `xml:"entry"`
}

type statusXMLItem struct {
	Path string `xml:"path,attr"`
	WC   struct {
		MovedFrom      string `xml:"moved-from,attr"`
		MovedTo        string `xml:"moved-to,attr"`
		Item           string `xml:"item,attr"`
		Props          string `xml:"props,attr"`
		Revision       int64  `xml:"revision,attr"`
		Copied         bool   `xml:"copied,attr"`
		Switched       bool   `xml:"switched,attr"`
		TreeConflicted bool   `xml:"tree-conflicted,attr"`
		Lock           struct {
			Owner string `xml:"owner"`
		} `xml:"lock"`
		Conflict struct {
			Old     string `xml:"prev-base-file"`
			New     string `xml:"prev-wc-file"`
			Working string `xml:"cur-base-file"`
		} `xml:"conflict"`
	} `xml:"wc-status"`
	Repos struct {
		Item  string `xml:"item,attr"`
		Props string `xml:"props,attr"`
	} `xml:"repos-status"`
}

// 统一读取普通目标和 changelist 中的状态项。
func ParseStatus(data []byte) ([]StatusEntry, error) {
	var doc statusDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 svn status: %w", err)
	}
	var entries []StatusEntry
	appendItems := func(items []statusXMLItem, changelist string) {
		for _, item := range items {
			entries = append(entries, StatusEntry{
				Path: item.Path, Changelist: changelist, MovedFrom: item.WC.MovedFrom, MovedTo: item.WC.MovedTo,
				Item: item.WC.Item, Properties: item.WC.Props, Revision: item.WC.Revision,
				Copied: item.WC.Copied, Switched: item.WC.Switched, TreeConflicted: item.WC.TreeConflicted,
				RepositoryItem: item.Repos.Item, RepositoryProps: item.Repos.Props, LockOwner: item.WC.Lock.Owner,
				ConflictOld: item.WC.Conflict.Old, ConflictNew: item.WC.Conflict.New, ConflictWorking: item.WC.Conflict.Working,
			})
		}
	}
	for _, target := range doc.Targets {
		appendItems(target.Entries, "")
		for _, changelist := range target.Changelists {
			appendItems(changelist.Entries, changelist.Name)
		}
	}
	for _, changelist := range doc.Changelists {
		appendItems(changelist.Entries, changelist.Name)
	}
	return entries, nil
}

func parseStatus(data []byte) ([]StatusEntry, error) { return ParseStatus(data) }

// SVN 对新建或替换节点使用文字表示没有工作修订。
func parseOptionalRevision(value string) (int64, error) {
	switch strings.TrimSpace(value) {
	case "", "Resource is not under version control.", "-1":
		return 0, nil
	default:
		return strconv.ParseInt(value, 10, 64)
	}
}

func parseProperties(data []byte) (map[string]string, error) {
	var doc struct {
		Targets []struct {
			Properties []struct {
				Name     string `xml:"name,attr"`
				Encoding string `xml:"encoding,attr"`
				Value    string `xml:",chardata"`
			} `xml:"property"`
		} `xml:"target"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 svn 属性: %w", err)
	}
	properties := make(map[string]string)
	for _, target := range doc.Targets {
		for _, property := range target.Properties {
			value := property.Value
			if property.Encoding == "base64" {
				decoded, err := base64.StdEncoding.DecodeString(value)
				if err != nil {
					return nil, fmt.Errorf("解析属性 %s: %w", property.Name, err)
				}
				value = string(decoded)
			} else if property.Encoding != "" {
				return nil, fmt.Errorf("未知属性编码 %q", property.Encoding)
			}
			properties[property.Name] = value
		}
	}
	return properties, nil
}

type logDocument struct {
	Entries []logXMLItem `xml:"logentry"`
}

type logXMLItem struct {
	Revision int64  `xml:"revision,attr"`
	Author   string `xml:"author"`
	Date     string `xml:"date"`
	Message  string `xml:"msg"`
	Paths    []struct {
		Action       string `xml:"action,attr"`
		CopyFromPath string `xml:"copyfrom-path,attr"`
		CopyFromRev  string `xml:"copyfrom-rev,attr"`
		Path         string `xml:",chardata"`
	} `xml:"paths>path"`
}

func parseLog(data []byte) ([]LogEntry, error) {
	var doc logDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 svn log: %w", err)
	}
	entries := make([]LogEntry, 0, len(doc.Entries))
	for _, item := range doc.Entries {
		entry := LogEntry{Revision: item.Revision, Author: item.Author, Message: item.Message}
		if item.Date != "" {
			entry.Date, _ = time.Parse(time.RFC3339Nano, item.Date)
		}
		for _, changed := range item.Paths {
			copyRev, _ := strconv.ParseInt(changed.CopyFromRev, 10, 64)
			entry.Paths = append(entry.Paths, ChangedPath{
				Action:       changed.Action,
				Path:         changed.Path,
				CopyFromPath: changed.CopyFromPath,
				CopyFromRev:  copyRev,
			})
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

type listDocument struct {
	Lists []struct {
		Path    string        `xml:"path,attr"`
		Entries []listXMLItem `xml:"entry"`
	} `xml:"list"`
}

type listXMLItem struct {
	Kind   string `xml:"kind,attr"`
	Name   string `xml:"name"`
	Size   int64  `xml:"size"`
	Commit struct {
		Revision int64  `xml:"revision,attr"`
		Author   string `xml:"author"`
		Date     string `xml:"date"`
	} `xml:"commit"`
}

func parseList(data []byte) ([]ListEntry, error) {
	var doc listDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 svn list: %w", err)
	}

	var entries []ListEntry
	for _, list := range doc.Lists {
		for _, item := range list.Entries {
			entry := ListEntry{
				Name:     item.Name,
				Kind:     item.Kind,
				Size:     item.Size,
				Revision: item.Commit.Revision,
				Author:   item.Commit.Author,
			}
			if item.Commit.Date != "" {
				entry.Date, _ = time.Parse(time.RFC3339Nano, item.Commit.Date)
			}
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

type blameDocument struct {
	Targets []struct {
		Path    string         `xml:"path,attr"`
		Entries []blameXMLItem `xml:"entry"`
	} `xml:"target"`
}

type blameXMLItem struct {
	Line   int64 `xml:"line-number,attr"`
	Commit struct {
		Revision int64  `xml:"revision,attr"`
		Author   string `xml:"author"`
		Date     string `xml:"date"`
	} `xml:"commit"`
	Merged struct {
		Path     string `xml:"path,attr"`
		Revision int64  `xml:"revision,attr"`
	} `xml:"merged"`
}

func parseBlame(data []byte) ([]BlameLine, error) {
	var doc blameDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 svn blame: %w", err)
	}

	var lines []BlameLine
	for _, target := range doc.Targets {
		for _, item := range target.Entries {
			line := BlameLine{
				Line:           item.Line,
				Revision:       item.Commit.Revision,
				Author:         item.Commit.Author,
				MergedPath:     item.Merged.Path,
				MergedRevision: item.Merged.Revision,
			}
			if item.Commit.Date != "" {
				line.Date, _ = time.Parse(time.RFC3339Nano, item.Commit.Date)
			}
			lines = append(lines, line)
		}
	}
	return lines, nil
}
