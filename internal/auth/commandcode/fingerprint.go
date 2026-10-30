package commandcode

// The values below reproduce what one pinned Command Code CLI release sends.
const (
	// CLIVersion is the command-code release the request values follow. The
	// gateway refuses versions below 0.18.10.
	CLIVersion = "1.88.2"
	// UserAgent is the User-Agent the CLI sends to the gateway.
	UserAgent = "cli"
	// CLIEnvironment is the value of the x-cli-environment header.
	CLIEnvironment = "production"
)
