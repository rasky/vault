package command

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/helper/kv-builder"
	"github.com/hashicorp/vault/helper/password"
	"github.com/mitchellh/mapstructure"
	"github.com/ryanuber/columnize"
)

// AuthHandler is the interface that any auth handlers must implement
// to enable auth via the CLI.
type AuthHandler interface {
	Auth(*api.Client, map[string]string) (string, error)
	Help() string
}

// AuthCommand is a Command that handles authentication.
type AuthCommand struct {
	Meta

	Handlers map[string]AuthHandler
}

func (c *AuthCommand) Run(args []string) int {
	var method string
	var methods, methodHelp bool
	flags := c.Meta.FlagSet("auth", FlagSetDefault)
	flags.BoolVar(&methods, "methods", false, "")
	flags.BoolVar(&methodHelp, "method-help", false, "")
	flags.StringVar(&method, "method", "", "method")
	flags.Usage = func() { c.Ui.Error(c.Help()) }
	if err := flags.Parse(args); err != nil {
		return 1
	}

	if methods {
		return c.listMethods()
	}

	args = flags.Args()
	if method == "" && len(args) < 1 {
		flags.Usage()
		c.Ui.Error("\nError: auth expects at least one argument")
		return 1
	}

	tokenHelper, err := c.TokenHelper()
	if err != nil {
		c.Ui.Error(fmt.Sprintf(
			"Error initializing token helper: %s\n\n"+
				"Please verify that the token helper is available and properly\n"+
				"configured for your system. Please refer to the documentation\n"+
				"on token helpers for more information.",
			err))
		return 1
	}

	// token is where the final token will go
	handler := c.Handlers[method]
	if method == "" {
		token := ""
		if len(args) > 0 {
			token = args[0]
		}

		handler = &tokenAuthHandler{Token: token}
		args = nil
	}

	if handler == nil {
		methods := make([]string, 0, len(c.Handlers))
		for k, _ := range c.Handlers {
			methods = append(methods, k)
		}
		sort.Strings(methods)

		c.Ui.Error(fmt.Sprintf(
			"Unknown authentication method: %s\n\n"+
				"Please use a supported authentication method. The list of supported\n"+
				"authentication methods is shown below. Note that this list may not\n"+
				"be exhaustive: Vault may support other auth methods. For auth methods\n"+
				"unsupported by the CLI, please use the HTTP API.\n\n"+
				"%s",
			method,
			strings.Join(methods, ", ")))
		return 1
	}

	if methodHelp {
		c.Ui.Output(handler.Help())
		return 0
	}

	var vars map[string]string
	if len(args) > 0 {
		builder := kvbuilder.Builder{Stdin: os.Stdin}
		if err := builder.Add(args...); err != nil {
			c.Ui.Error(err.Error())
			return 1
		}

		if err := mapstructure.Decode(builder.Map(), &vars); err != nil {
			c.Ui.Error(fmt.Sprintf("Error parsing options: %s", err))
			return 1
		}
	}

	// Build the client so we can auth
	client, err := c.Client()
	if err != nil {
		c.Ui.Error(fmt.Sprintf(
			"Error initializing client to auth: %s", err))
		return 1
	}

	// Authenticate
	token, err := handler.Auth(client, vars)
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}

	// Store the token!
	if err := tokenHelper.Store(token); err != nil {
		c.Ui.Error(fmt.Sprintf(
			"Error storing token: %s\n\n"+
				"Authentication was not successful and did not persist.\n"+
				"Please reauthenticate, or fix the issue above if possible.",
			err))
		return 1
	}

	// Build the client again so it can read the token we just wrote
	client, err = c.Client()
	if err != nil {
		c.Ui.Error(fmt.Sprintf(
			"Error initializing client to verify the token: %s", err))
		return 1
	}

	// Verify the token
	secret, err := client.Logical().Read("auth/token/lookup-self")
	if err != nil {
		c.Ui.Error(fmt.Sprintf(
			"Error validating token: %s", err))
		return 1
	}
	if secret == nil {
		c.Ui.Error(fmt.Sprintf("Error: Invalid token"))
		return 1
	}

	// Get the policies we have
	policiesRaw, ok := secret.Data["policies"]
	if !ok {
		policiesRaw = []string{"unknown"}
	}
	var policies []string
	for _, v := range policiesRaw.([]interface{}) {
		policies = append(policies, v.(string))
	}

	c.Ui.Output(fmt.Sprintf(
		"Successfully authenticated! The policies that are associated\n"+
			"with this token are listed below:\n\n%s",
		strings.Join(policies, ", "),
	))

	return 0
}

func (c *AuthCommand) listMethods() int {
	client, err := c.Client()
	if err != nil {
		c.Ui.Error(fmt.Sprintf(
			"Error initializing client: %s", err))
		return 1
	}

	auth, err := client.Sys().ListAuth()
	if err != nil {
		c.Ui.Error(fmt.Sprintf(
			"Error reading auth table: %s", err))
		return 1
	}

	paths := make([]string, 0, len(auth))
	for path, _ := range auth {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	columns := []string{"Path | Type | Description"}
	for _, k := range paths {
		a := auth[k]
		columns = append(columns, fmt.Sprintf(
			"%s | %s | %s", k, a.Type, a.Description))
	}

	c.Ui.Output(columnize.SimpleFormat(columns))
	return 0
}

func (c *AuthCommand) Synopsis() string {
	return "Prints information about how to authenticate with Vault"
}

func (c *AuthCommand) Help() string {
	helpText := `
Usage: vault auth [options] [token or config...]

  Authenticate with Vault with the given token or via any supported
  authentication backend.

  If no -method is specified, then the token is expected. If it is not
  given on the command-line, it will be asked via user input. If the
  token is "-", it will be read from stdin.

  By specifying -method, alternate authentication methods can be used
  such as OAuth or TLS certificates. For these, additional values for
  configuration can be specified with "key=value" pairs just like
  "vault write".

General Options:

  -address=addr           The address of the Vault server.

  -ca-cert=path           Path to a PEM encoded CA cert file to use to
                          verify the Vault server SSL certificate.

  -ca-path=path           Path to a directory of PEM encoded CA cert files
                          to verify the Vault server SSL certificate. If both
                          -ca-cert and -ca-path are specified, -ca-path is used.

  -insecure               Do not verify TLS certificate. This is highly
                          not recommended.

Auth Options:

  -method=name      Outputs help for the authentication method with the given
                    name for the remote server. If this authentication method
                    is not available, exit with code 1.

  -method-help      If set, the help for the selected method will be shown.

  -methods          List the available auth methods.

`
	return strings.TrimSpace(helpText)
}

// tokenAuthHandler handles retrieving the token from the command-line.
type tokenAuthHandler struct {
	Token string
}

func (h *tokenAuthHandler) Auth(*api.Client, map[string]string) (string, error) {
	token := h.Token
	if token == "" {
		var err error

		// No arguments given, read the token from user input
		fmt.Printf("Token (will be hidden): ")
		token, err = password.Read(os.Stdin)
		fmt.Printf("\n")
		if err != nil {
			return "", fmt.Errorf(
				"Error attempting to ask for token. The raw error message\n"+
					"is shown below, but the most common reason for this error is\n"+
					"that you attempted to pipe a value into auth. If you want to\n"+
					"pipe the token, please pass '-' as the token argument.\n\n"+
					"Raw error: %s", err)
		}
	}

	if token == "" {
		return "", fmt.Errorf(
			"A token must be passed to auth. Please view the help\n" +
				"for more information.")
	}

	return token, nil
}

func (h *tokenAuthHandler) Help() string {
	help := `
No method selected with the "-method" flag, so the "auth" command assumes
you'll be using raw token authentication. For this, specify the token to
authenticate as as the parameter to "vault auth". Example:

    vault auth 123456

The token used to authenticate must come from some other source. A root
token is created when Vault is first initialized. After that, subsequent
tokens are created via the API or command line interface (with the
"token"-prefixed commands).
`

	return strings.TrimSpace(help)
}
