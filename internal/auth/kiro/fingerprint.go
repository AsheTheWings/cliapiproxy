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
	// agent sends when it runs on an API key. The agent's own value names the
	// host's kernel release after os/linux; the server names none.
	ControlPlaneUserAgent = "aws-sdk-js/1.0.0 ua/2.1 os/linux lang/js md/nodejs#22.22.2 api/kirocontrolplanebearer#1.0.0 m/N,E " +
		InferenceUserAgent
	// ControlPlaneAmzUserAgent is the X-Amz-User-Agent of catalog requests.
	ControlPlaneAmzUserAgent = "aws-sdk-js/1.0.0 " + InferenceUserAgent
)
