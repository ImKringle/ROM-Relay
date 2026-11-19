package torrent

import (
	"path"
	"regexp"
	"strings"
)

// Release is a file name split the way No-Intro / Redump names are written:
// "Title (Region, Region) (En,Fr) (Rev 1) [b]". Title is what the game search
// matches; the tags feed the region / language / other filters, so searching
// "Japan" in the game box never matches a region tag.
type Release struct {
	Title     string   `json:"title"`
	Regions   []string `json:"regions,omitempty"`
	Languages []string `json:"languages,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

var (
	groupRe    = regexp.MustCompile(`\(([^()]*)\)|\[([^\[\]]*)\]`)
	languageRe = regexp.MustCompile(`^[A-Z][a-z](-[A-Za-z]{2,4})?$`)
	regions    = map[string]bool{}
)

func init() {
	for _, r := range strings.Split("World,USA,Europe,Japan,Korea,China,Taiwan,Hong Kong,Asia,Australia,New Zealand,Brazil,Canada,Mexico,Argentina,France,Germany,Italy,Spain,Portugal,Netherlands,Belgium,Sweden,Norway,Denmark,Finland,Scandinavia,UK,Ireland,Russia,Poland,Greece,Austria,Switzerland,India,Latin America,United Arab Emirates,South Africa,Unknown", ",") {
		regions[r] = true
	}
}

// ParseRelease splits a torrent file path's base name into title and tags.
func ParseRelease(filePath string) Release {
	name := path.Base(filePath)
	name = strings.TrimSuffix(name, path.Ext(name))
	var r Release
	for _, m := range groupRe.FindAllStringSubmatch(name, -1) {
		inner := strings.TrimSpace(m[1] + m[2])
		if inner == "" {
			continue
		}
		items := strings.Split(inner, ",")
		for i := range items {
			items[i] = strings.TrimSpace(items[i])
		}
		switch {
		case m[1] != "" && all(items, func(s string) bool { return regions[s] }):
			r.Regions = append(r.Regions, items...)
		case m[1] != "" && all(items, languageRe.MatchString):
			r.Languages = append(r.Languages, items...)
		default:
			r.Tags = append(r.Tags, inner)
		}
	}
	r.Title = strings.Join(strings.Fields(groupRe.ReplaceAllString(name, " ")), " ")
	if r.Title == "" {
		r.Title = name
	}
	return r
}

func all(items []string, ok func(string) bool) bool {
	for _, s := range items {
		if !ok(s) {
			return false
		}
	}
	return len(items) > 0
}
