package server

import "encoding/hex"

const sourceRepository = "https://github.com/ZelCore-io/blockbook"

func sourceLink(revision string) (string, string) {
	if len(revision) == 40 {
		if _, err := hex.DecodeString(revision); err == nil {
			return sourceRepository + "/tree/" + revision, "Source"
		}
	}
	return sourceRepository, "Source (development build)"
}
