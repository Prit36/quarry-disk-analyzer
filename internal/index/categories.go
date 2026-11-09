package index

import (
	"cmp"
	"slices"
	"strings"
)

// Category groups extensions by what the files are, so storage can be
// described as "videos" or "installers" rather than forty extensions. The
// table is fixed and small; categories are derived from extension totals
// the views already compute, never from a separate walk of the index.
type Category struct {
	Key        string // used in the search syntax: cat:videos
	Name       string
	Extensions []string // lower case, with the dot
}

// Other is the category of every extension the table does not list.
const Other = "Other"

var Categories = []Category{
	{"videos", "Videos", []string{".mp4", ".mkv", ".mov", ".avi", ".wmv", ".m4v", ".webm", ".flv", ".mpg", ".mpeg", ".m2ts", ".mts", ".3gp", ".vob", ".ogv", ".braw", ".r3d"}},
	{"images", "Images", []string{".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tif", ".tiff", ".webp", ".heic", ".heif", ".avif", ".raw", ".cr2", ".cr3", ".nef", ".arw", ".dng", ".orf", ".rw2", ".psd", ".svg", ".ico", ".jxl", ".exr", ".hdr"}},
	{"audio", "Audio", []string{".mp3", ".wav", ".flac", ".aac", ".m4a", ".ogg", ".opus", ".wma", ".aiff", ".aif", ".alac", ".mid", ".midi"}},
	{"archives", "Archives", []string{".zip", ".7z", ".rar", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".zst", ".lz", ".lzma", ".cab", ".arj", ".z"}},
	{"installers", "Installers", []string{".msi", ".msix", ".msixbundle", ".appx", ".appxbundle", ".msp", ".msu", ".apk", ".pkg", ".dmg", ".deb", ".rpm"}},
	{"diskimages", "Disk images", []string{".iso", ".img", ".vhd", ".vhdx", ".vmdk", ".vdi", ".qcow2", ".wim", ".esd", ".nrg", ".avhdx"}},
	{"documents", "Documents", []string{".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".odt", ".ods", ".odp", ".rtf", ".txt", ".md", ".csv", ".epub", ".pages", ".numbers", ".one", ".xps"}},
	{"code", "Code", []string{".go", ".c", ".h", ".cpp", ".hpp", ".cc", ".cs", ".java", ".kt", ".py", ".js", ".mjs", ".ts", ".tsx", ".jsx", ".rs", ".rb", ".php", ".swift", ".m", ".sh", ".ps1", ".bat", ".cmd", ".json", ".xml", ".yaml", ".yml", ".toml", ".html", ".htm", ".css", ".scss", ".sql", ".lua", ".pyc", ".class", ".o", ".obj", ".pdb", ".ipynb"}},
	{"programs", "Programs and libraries", []string{".exe", ".dll", ".sys", ".ocx", ".so", ".dylib", ".jar", ".node", ".mui", ".efi", ".drv", ".winmd"}},
	{"databases", "Databases", []string{".db", ".sqlite", ".sqlite3", ".mdb", ".accdb", ".mdf", ".ldf", ".ndf", ".edb", ".pst", ".ost"}},
	{"temporary", "Logs and temporary files", []string{".log", ".tmp", ".temp", ".bak", ".old", ".dmp", ".etl", ".evtx", ".cache", ".crdownload", ".part", ".partial"}},
}

// categoryOf maps a lower-case extension to its category. Where a table
// lists an extension twice (.ts is both a video stream and TypeScript), the
// first category wins.
var categoryOf = func() map[string]int {
	m := map[string]int{}
	for i, c := range Categories {
		for _, e := range c.Extensions {
			if len(e) > 16 || strings.ToLower(e) != e {
				panic("category extensions must be short and lower case: " + e) // Filter.matchCategory relies on it
			}
			if _, ok := m[e]; !ok {
				m[e] = i
			}
		}
	}
	return m
}()

// CategoryOf names the category of a lower-case extension with its dot, as
// FileType.Extension holds it; "(no extension)" and unknown ones are Other.
func CategoryOf(ext string) string {
	if i, ok := categoryOf[ext]; ok {
		return Categories[i].Name
	}
	return Other
}

// CategoryKey is CategoryOf's key for the search syntax, "" for Other.
func CategoryKey(ext string) string {
	if i, ok := categoryOf[ext]; ok {
		return Categories[i].Key
	}
	return ""
}

// FindCategory resolves a cat: value: a key or name, any case, singular or
// plural, with or without spaces ("video", "Disk images", "diskimage").
func FindCategory(value string) (Category, bool) {
	v := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	if v == "" {
		return Category{}, false
	}
	for _, c := range Categories {
		name := strings.ToLower(strings.ReplaceAll(c.Name, " ", ""))
		if v == c.Key || v == name || v+"s" == c.Key || strings.HasPrefix(c.Key, v) && len(v) >= 3 {
			return c, true
		}
	}
	return Category{}, false
}

// CategoryTotal is the files of one category.
type CategoryTotal struct {
	Key, Name               string
	Bytes, Allocated, Count uint64
	// Top lists the category's largest extensions, largest first.
	Top []string
}

// SummarizeCategories folds extension totals into categories, largest first
// by the chosen metric; Other comes last whatever its size.
func SummarizeCategories(types []FileType, allocated bool) []CategoryTotal {
	byName := map[string]*CategoryTotal{}
	type ext struct {
		name  string
		value uint64
	}
	tops := map[string][]ext{}
	for _, t := range types {
		name, key := CategoryOf(t.Extension), CategoryKey(t.Extension)
		total := byName[name]
		if total == nil {
			total = &CategoryTotal{Key: key, Name: name}
			byName[name] = total
		}
		total.Bytes += t.Bytes
		total.Allocated += t.Allocated
		total.Count += t.Count
		v := t.Bytes
		if allocated {
			v = t.Allocated
		}
		tops[name] = append(tops[name], ext{t.Extension, v})
	}
	out := make([]CategoryTotal, 0, len(byName))
	for name, total := range byName {
		list := tops[name]
		slices.SortFunc(list, func(a, b ext) int {
			if c := cmp.Compare(b.value, a.value); c != 0 {
				return c
			}
			return strings.Compare(a.name, b.name)
		})
		for i := 0; i < len(list) && i < 4; i++ {
			total.Top = append(total.Top, list[i].name)
		}
		out = append(out, *total)
	}
	slices.SortFunc(out, func(a, b CategoryTotal) int {
		if (a.Name == Other) != (b.Name == Other) {
			if a.Name == Other {
				return 1
			}
			return -1
		}
		x, y := a.Bytes, b.Bytes
		if allocated {
			x, y = a.Allocated, b.Allocated
		}
		if c := cmp.Compare(y, x); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}
