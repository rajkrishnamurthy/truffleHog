package amadeus

import (
	"context"
	"io/ioutil"
	"net/http"
	"regexp"
	"strings"

	"github.com/trufflesecurity/trufflehog/v3/pkg/common"
	"github.com/trufflesecurity/trufflehog/v3/pkg/detectors"
	"github.com/trufflesecurity/trufflehog/v3/pkg/pb/detectorspb"
)

type Scanner struct{}

// Ensure the Scanner satisfies the interface at compile time
var _ detectors.Detector = (*Scanner)(nil)

var (
	client = common.SaneHttpClient()

	//Make sure that your group is surrounded in boundry characters such as below to reduce false positives
	keyPat    = regexp.MustCompile(detectors.PrefixRegex([]string{"amadeus"}) + `\b([0-9A-Za-z]{32})\b`)
	secretPat = regexp.MustCompile(detectors.PrefixRegex([]string{"amadeus"}) + `\b([0-9A-Za-z]{16})\b`)
)

// Keywords are used for efficiently pre-filtering chunks.
// Use identifiers in the secret preferably, or the provider name.
func (s Scanner) Keywords() []string {
	return []string{"amadeus"}
}

// FromData will find and optionally verify Amadeus secrets in a given set of bytes.
func (s Scanner) FromData(ctx context.Context, verify bool, data []byte) (results []detectors.Result, err error) {
	dataStr := string(data)

	matches := keyPat.FindAllStringSubmatch(dataStr, -1)
	secretMatches := secretPat.FindAllStringSubmatch(dataStr, -1)

	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		resMatch := strings.TrimSpace(match[1])
		for _, secretMatch := range secretMatches {
			if len(secretMatch) != 2 {
				continue
			}
			resSecret := strings.TrimSpace(secretMatch[1])

			s1 := detectors.Result{
				DetectorType: detectorspb.DetectorType_Amadeus,
				Raw:          []byte(resMatch),
			}

			if verify {
				payload := strings.NewReader("grant_type=client_credentials&client_id=" + resMatch + "&client_secret=" + resSecret)

				req, _ := http.NewRequestWithContext(ctx, "POST", "https://test.api.amadeus.com/v1/security/oauth2/token", payload)
				req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
				res, err := client.Do(req)
				if err == nil {
					defer res.Body.Close()
					bodyBytes, _ := ioutil.ReadAll(res.Body)
					body := string(bodyBytes)
					if (res.StatusCode >= 200 && res.StatusCode < 300) && strings.Contains(body, "access_token") {
						s1.Verified = true
					} else {
						//This function will check false positives for common test words, but also it will make sure the key appears 'random' enough to be a real key
						if detectors.IsKnownFalsePositive(resMatch, detectors.DefaultFalsePositives, true) {
							continue
						}
					}
				}
			}

			results = append(results, s1)
		}
	}

	return detectors.CleanResults(results), nil
}
