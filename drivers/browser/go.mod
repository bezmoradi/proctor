module proctor/drivers/browser

go 1.27.4

require (
	github.com/bezmoradi/proctor/protocol v0.0.0
	github.com/chromedp/cdproto v0.160.6
	github.com/chromedp/chromedp v0.24.0
)

require github.com/go-json-experiment/json v0.0.0-20260213210345-44df1a37e875 // indirect

replace github.com/bezmoradi/proctor/protocol => ../../protocol
