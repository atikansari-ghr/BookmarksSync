package backup

import (
	"path"
	"strings"
)

func mountTypeFromInfo(pathValue, contents string) string {
	pathValue = path.Clean(pathValue)
	best, kind := -1, ""
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if separator < 0 || separator+1 >= len(fields) || len(fields) < 5 {
			continue
		}
		mountPoint := path.Clean(decodeMountInfoPath(fields[4]))
		if !mountedWithin(mountPoint, pathValue) {
			continue
		}
		if len(mountPoint) >= best {
			best = len(mountPoint)
			kind = fields[separator+1]
		}
	}
	return kind
}

func mountedWithin(mountPoint, target string) bool {
	if mountPoint == "/" {
		return strings.HasPrefix(target, "/")
	}
	return target == mountPoint || strings.HasPrefix(target, strings.TrimSuffix(mountPoint, "/")+"/")
}

func decodeMountInfoPath(value string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(value)
}
