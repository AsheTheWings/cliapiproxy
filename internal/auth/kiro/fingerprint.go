package kiro

// The strings below reproduce what one pinned Kiro CLI release sends.
const (
	// CLIVersion is the kiro-cli release the request values follow.
	CLIVersion = "2.35.1"
	// AgentVersion is the Kiro agent (KAS) release the request values follow.
	AgentVersion = "0.66.70"

	// InferenceUserAgent is the User-Agent of inference requests.
	InferenceUserAgent = "KiroCLI/" + CLIVersion + " KAS/" + AgentVersion +
		" os/linux md/appVersion-" + CLIVersion + " app/AmazonQ-For-CLI acp-client/kiro-cli-non-interactive"

	// ControlPlaneUserAgent is the User-Agent of catalog requests, which the
	// launcher sends.
	ControlPlaneUserAgent = "aws-sdk-rust/1.6.0 ua/2.1 api/kirocontrolplanebearer/0.1.0 os/linux lang/rust/1.94.1 md/appVersion-" +
		CLIVersion + " app/AmazonQ-For-CLI"
	// ControlPlaneAmzUserAgent is the X-Amz-User-Agent of catalog requests.
	ControlPlaneAmzUserAgent = "aws-sdk-rust/1.6.0 ua/2.1 api/kirocontrolplanebearer/0.1.0 os/linux lang/rust/1.94.1 m/F,C app/AmazonQ-For-CLI"
)
