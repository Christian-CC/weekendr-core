package weekendr

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
)

// computePhotoHash returns the MD5 digest of the file at filePath as a
// hex-encoded string. Used to populate PhotoIndexEntry.Hash so peers can
// detect file-level duplicates across devices.
func computePhotoHash(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
