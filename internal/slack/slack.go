package slack

import (
	"fmt"
	"gopkg.in/yaml.v2"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

type Client struct {
	slackClient  *slack.Client
	socketClient *socketmode.Client
	cfg          *Config
}

// NewSlackClient creates a new slack client.
func NewSlackClient(configFile, botToken, appToken string) *Client {
	if configFile == "" {
		configFile = os.Getenv("BASHBOT_CONFIG_FILEPATH")
	}
	cfg, err := loadConfigFile(configFile)
	if err != nil {
		log.WithError(err).Fatal("Problem loading config-file")
	}
	if botToken == "" {
		botToken = os.Getenv("SLACK_BOT_TOKEN")
	}
	if appToken == "" {
		appToken = os.Getenv("SLACK_APP_TOKEN")
	}
	if botToken == "" {
		log.Fatal("Must define a slack bot token")
	}
	if appToken == "" {
		log.Fatal("Must define a slack app token")
	}
	api := slack.New(botToken, slack.OptionAppLevelToken(appToken))
	client := socketmode.New(api)
	return &Client{
		cfg:          cfg,
		socketClient: client,
		slackClient:  api,
	}
}

// loadConfigFile is a helper function for loading bashbot yaml
// configuration file into Config struct.
func loadConfigFile(filePath string) (*Config, error) {
	fileContents, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var config Config
	err = yaml.Unmarshal(fileContents, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

// Run runs the slack socketmode client on background.
func (c *Client) Run() {
	go c.socketClient.Run()

	for event := range c.socketClient.Events {
		switch event.Type {
		case socketmode.EventTypeEventsAPI:
			c.eventsAPIHandler(event)

		case socketmode.EventTypeConnected:
			log.Info("Bashbot is now connected to slack. Primary trigger: `" + c.cfg.Admins[0].Trigger + "`")

		case socketmode.EventTypeConnectionError:
			log.Error("Slack socket connection error")

		case socketmode.EventTypeErrorBadMessage:
			log.Error("Bad message received")
		}
	}
}

// eventsAPIHandler is a slack socket event handler for handling
// events API event.
func (c *Client) eventsAPIHandler(socketEvent socketmode.Event) error {
	event := socketEvent.Data.(slackevents.EventsAPIEvent)
	c.socketClient.Ack(*socketEvent.Request)
	switch event.Type {
	case slackevents.CallbackEvent:
		innerEvent := event.InnerEvent

		switch event := innerEvent.Data.(type) {
		case *slackevents.MessageEvent:
			if event.SubType == "bot_message" {
				break
			}
			c.processCommand(event)
		}
	default:
		return fmt.Errorf("unhandled event type: %s", event.Type)
	}
	return nil
}

// InstallVendorDependencies is a helper function for installing the
// vendor dependencies required by the current bashbot instance.
//
// In the process of installing the dependencies, the dependency installer
// executes the install command provided in the configuration file for each
// dependency.
func (c *Client) InstallVendorDependencies() {
	log.Debug("installing vendor dependencies")
	for i := 0; i < len(c.cfg.Dependencies); i++ {
		log.Info(c.cfg.Dependencies[i].Name)
		words := strings.Fields(strings.Join(c.cfg.Dependencies[i].Install, " "))
		var tcmd []string
		for index, element := range words {
			log.Debugf("%d: %s", index, element)
			tcmd = append(tcmd, element)
		}
		cmd := []string{"bash", "-c", "pushd vendor && " + strings.Join(tcmd, " ") + " && popd"}
		log.Debug(strings.Join(cmd, " "))
		log.Info(c.runShellCommands(cmd))
	}
}

// runShellCommands is a helper function for executing shell commands on
// the bashbot host machine.
//
//	 usage:
//			runShellCommands([]string{"bash", "-c", "apt-get install git && echo hello"})
//
// The first value in the array should be the command name e.g bash, sh etc
// while the other values will be treated as arguments.
func (c *Client) runShellCommands(cmdArgs []string) string {
	cmdOut, err := exec.Command(cmdArgs[0], cmdArgs[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("error running command:\n%s\nerror: %s", strings.Join(cmdArgs, " "), err.Error())
	}
	out := string(cmdOut)
	displayOut := regexp.MustCompile(`\s*\n`).ReplaceAllString(out, "\\n")
	log.Debug("Output from command: \n", displayOut)
	return out
}

// sendConfigMessageToChannel sends a message to the slack channel based on the
// messages configured in the bashbot config file.
//
// usage:
//
//	sendConfigMessageToChannel(cfg, client, "channelID", "processing_command", "try another command")
//
// The passalong parameter is an optional parameter because not all messages needs additional
// content(s) attached to the message sent.
func (c *Client) sendConfigMessageToChannel(channel, message, passalong string) {
	isActive := true
	responseMessage := message
	for i := 0; i < len(c.cfg.Messages); i++ {
		if c.cfg.Messages[i].Name == message {
			log.Debug(c.cfg.Messages[i].Name)
			isActive = c.cfg.Messages[i].Active
			responseMessage = c.cfg.Messages[i].Text
			if passalong != "" {
				responseMessage = fmt.Sprintf(c.cfg.Messages[i].Text, passalong)
			}
		}
	}
	if isActive {
		c.SendMessageToChannel(channel, responseMessage)
		return
	}
	log.Warn("Message suppressed by configuration")
	log.Warn(responseMessage)
}

// SendMessageToChannel sends a message to a slack channel.
func (c *Client) SendMessageToChannel(channel, msg string) {
	messageParams := slack.PostMessageParameters{UnfurlLinks: true, UnfurlMedia: true}
	channelID, _, err := c.slackClient.PostMessage(
		channel,
		slack.MsgOptionText(strings.Replace(msg, "\\n", "\n", -1), false),
		slack.MsgOptionUsername(c.cfg.Admins[0].AppName),
		slack.MsgOptionPostMessageParameters(messageParams),
	)
	if err != nil {
		log.Errorf("failed to send message to slack channel: %s", err.Error())
		return
	}
	log.Infof("Sent slack message[Channel:%s]: %s", channelID, msg)
}

// SendMessageToUser sends to message to a slack user in a slack channel.
func (c *Client) SendMessageToUser(channel, user, msg string) {
	messageParams := slack.PostMessageParameters{UnfurlLinks: true, UnfurlMedia: true}
	_, err := c.slackClient.PostEphemeral(
		channel,
		user,
		slack.MsgOptionText(strings.Replace(msg, "\\n", "\n", -1), false),
		slack.MsgOptionUsername(c.cfg.Admins[0].AppName),
		slack.MsgOptionPostMessageParameters(messageParams),
	)
	if err != nil {
		log.Error(err)
		return
	}
	log.Info("Sent ephemeral slack message[Channel:" + channel + "]: " + msg)
}

func truncateString(str string, num int) string {
	res := str
	if len(str) > num {
		if num > 3 {
			num -= 3
		}
		res = str[0:num] + "..."
	}
	return res
}

// getChannelNamesByType retrieves names of the channels monitored by bashbot
// by thier channel type.
//
// The available channel types are private_channel and public_channel.
func (c *Client) getChannelNamesByType(channelsID []string, channelType string) ([]string, []slack.Channel) {
	var names []string
	channels, _, err := c.socketClient.GetConversations(&slack.GetConversationsParameters{
		Limit: 1000,
		Types: []string{channelType},
	})
	if err != nil {
		log.Error(err)
		return nil, nil
	}
	for j := 0; j < len(channels); j++ {
		for i := range channelsID {
			if channelsID[i] == channels[j].ID {
				names = append(names, channels[j].Name)
			}
		}
	}
	return names, channels
}

// getChannelNames retreives the names of the channels monitored by bashbot
// using the channels id.
func (c *Client) getChannelNames(channelsID []string) []string {
	privateChannelNames, privateChannels := c.getChannelNamesByType(channelsID, "private_channel")
	log.Debugf("Number of private channels this bot is monitoring: %d", len(privateChannels))

	publicChannelNames, publicChannels := c.getChannelNamesByType(channelsID, "public_channel")
	log.Debugf("Number of public channels this bot is monitoring: %d", len(publicChannels))

	names := append(privateChannelNames, publicChannelNames...)
	if len(names) > 0 {
		return names
	}
	return []string{"all"}
}

func (c *Client) processCommand(event *slackevents.MessageEvent) bool {
	matchTrigger := fmt.Sprintf("(?i)^%s .", c.cfg.Admins[0].Trigger)
	cmdPattern := regexp.MustCompile(matchTrigger)
	if !cmdPattern.MatchString(event.Text) {
		return false
	}
	log.Infof("command detected: `%s`", event.Text)
	log.Debug(event)
	log.Infof("Channel: %s", event.Channel)
	log.Infof("User: %s", event.User)
	log.Infof("Timestamp: %s", event.TimeStamp)

	words := strings.Fields(event.Text)
	var cmd []string
	for index, element := range words {
		element = regexp.MustCompile(`<http(.*)>`).ReplaceAllString(element, "http$1")
		element = regexp.MustCompile(`“|”`).ReplaceAllString(element, "\"")
		element = regexp.MustCompile(`‘|’`).ReplaceAllString(element, "'")
		log.Infof("%d: %s", index, element)
		if index > 1 {
			cmd = append(cmd, element)
		}
	}

	tool := c.cfg.GetTool(words[1])
	switch words[1] {
	case tool.Trigger:
		c.sendConfigMessageToChannel(event.Channel, "processing_command", "")
		return c.processValidCommand(cmd, tool, event.Channel, event.User, event.TimeStamp)
	case "exit":
		if len(words) == 3 {
			switch words[2] {
			case "0":
				c.SendMessageToChannel(event.Channel, "exiting: success")
				os.Exit(0)
			default:
				c.SendMessageToChannel(event.Channel, "exiting: failure")
				os.Exit(1)
			}
		}
		c.SendMessageToChannel(event.Channel, "My battery is low and it's getting dark.")
		os.Exit(0)
		return false
	default:
		c.sendConfigMessageToChannel(event.Channel, "command_not_found", "")
		return false
	}
}

// validateRequiredEnvVars is a helper function for checking if required environment variables
// are available for bashbot.
//
// If any required environment variable is not set, it returns a missingenvvar error to the
// slack bashbot client.
func (c *Client) validateRequiredEnvVars(channel string, tool Tool) error {
	for _, envvar := range tool.Envvars {
		// Ignore runtime environment variables
		if os.Getenv(envvar) == "" && envvar != "TRIGGERED_AT" && envvar != "TRIGGERED_USER_ID" && envvar != "TRIGGERED_USER_NAME" && envvar != "TRIGGERED_CHANNEL_ID" && envvar != "TRIGGERED_CHANNEL_NAME" {
			c.sendConfigMessageToChannel(channel, "missingenvvar", envvar)
			return fmt.Errorf("missing environment variable '%s'", envvar)
		}
	}
	return nil
}

// validateRequiredDependencies is a helper function for checking if required software dependencies
// are available for bashbot.
//
// If any required software dependency is not installed on the host machine, it returns a
// missingdependency error to the slack bashbot client.
func (c *Client) validateRequiredDependencies(channel string, tool Tool) error {
	for _, dependency := range tool.Dependencies {
		if _, err := exec.LookPath(dependency); err != nil {
			c.sendConfigMessageToChannel(channel, "missingdependency", dependency)
			return fmt.Errorf("missing application/software dependency '%s'", dependency)
		}
	}
	return nil
}

func (c *Client) processValidCommand(cmds []string, tool Tool, channel, user, timestamp string) bool {
	err := c.validateRequiredEnvVars(channel, tool)
	if err != nil {
		return false
	}
	err = c.validateRequiredDependencies(channel, tool)
	if err != nil {
		return false
	}
	// inject email if exists in command
	thisUser, err := c.slackClient.GetUserInfo(user)
	if err != nil {
		log.Info(fmt.Printf("%s\n", err))
		return true
	}
	reEmail := regexp.MustCompile(`\${email}`)
	commandJoined := reEmail.ReplaceAllLiteralString(strings.Join(tool.Command, " "), thisUser.Profile.Email)

	log.Infof(" ----> Param Name:        %s", tool.Name)
	log.Infof(" ----> Param Description: %s", tool.Description)
	log.Infof(" ----> Param Log:         %s", strconv.FormatBool(tool.Log))
	log.Infof(" ----> Param Help:        %s", tool.Help)
	log.Infof(" ----> Param Trigger:     %s", tool.Trigger)
	log.Infof(" ----> Param Location:    %s", tool.Location)
	log.Infof(" ----> Param Command:     %s", commandJoined)
	log.Infof(" ----> Param Ephemeral:   %s", strconv.FormatBool(tool.Ephemeral))
	log.Infof(" ----> Param Response:    %s", tool.Response)
	validParams := make([]bool, len(tool.Parameters))
	var tmpHelp string
	authorized := false
	var allowedChannels []string = c.getChannelNames(tool.Permissions)
	if c.cfg.Admins[0].PrivateChannelId == channel {
		authorized = true
	} else {
		for j := 0; j < len(tool.Permissions); j++ {
			log.Debugf(" ----> Param Permissions[%d]: %s", j, tool.Permissions[j])
			if tool.Permissions[j] == channel || tool.Permissions[j] == "all" {
				authorized = true
			}
		}
	}

	// Show help if the first parameter is "help"
	cmdHelp := fmt.Sprintf("``` ====> %s [Allowed In: %s] <====\n%s\n%s%s```", tool.Name, strings.Join(allowedChannels, ", "), tool.Description, tool.Help, tmpHelp)
	if len(cmds) > 0 {
		for j := 0; j < len(cmds); j++ {
			if cmds[j] == "help" {
				c.SendMessageToChannel(channel, cmdHelp)
				return true
			}
		}
	}

	if !authorized {
		c.sendConfigMessageToChannel(channel, "unauthorized", strings.Join(allowedChannels, ", "))
		c.SendMessageToChannel(channel, cmdHelp)
		c.logToChannel(channel, user, tool.Trigger+" "+strings.Join(cmds, " "))
		return true
	}

	if len(tool.Parameters) > 0 {
		log.Debug(" ----> Param Parameters Count: " + strconv.Itoa(len(tool.Parameters)))
		for j := range tool.Parameters {
			log.Debug(" ----> Param Parameters[" + strconv.Itoa(j) + "]: " + tool.Parameters[j].Name)
			derivedSource := tool.Parameters[j].Source
			tmpHelp = fmt.Sprintf("%s\n%s: [%s%s]", tmpHelp, tool.Parameters[j].Name, strings.Join(tool.Parameters[j].Allowed, "|"), tool.Parameters[j].Description)
			if len(derivedSource) > 0 {
				log.Debug("Deriving allowed parameters: " + strings.Join(derivedSource, " "))
				allowedOut := strings.Split(c.runShellCommands([]string{"bash", "-c", "cd " + tool.Location + " && " + strings.Join(derivedSource, " ")}), "\n")
				tool.Parameters[j].Allowed = append(tool.Parameters[j].Allowed, allowedOut...)
			}
		}
	}

	if tool.Log {
		c.logToChannel(channel, user, tool.Trigger+" "+strings.Join(cmds, " "))
	}

	// Validate parameters against whitelist
	if len(tool.Parameters) > 0 {
		for j := 0; j < len(tool.Parameters); j++ {
			log.Debug(" ====> Param Name: " + tool.Parameters[j].Name)
			validParams[j] = false

			if len(tool.Parameters[j].Match) > 0 {
				log.Debug(" ====> Parameter[" + strconv.Itoa(j) + "].Regex: " + tool.Parameters[j].Match)
				restOfCommand := strings.Join(cmds[j:], " ")
				if regexp.MustCompile(tool.Parameters[j].Match).MatchString(restOfCommand) {
					log.Debug("Parameter(s): '" + restOfCommand + "' matches regex: '" + tool.Parameters[j].Match + "'")
					validParams[j] = true
				} else {
					log.Debug("Parameter: " + cmds[j] + " does not match regex: " + tool.Parameters[j].Match)
				}
				continue
			}
			for h := 0; h < len(tool.Parameters[j].Allowed); h++ {
				log.Debug(" ====> Parameter[" + strconv.Itoa(j) + "].Allowed[" + strconv.Itoa(h) + "]: " + tool.Parameters[j].Allowed[h])
				if j < len(cmds) {
					if tool.Parameters[j].Allowed[h] == cmds[j] {
						validParams[j] = true
					}
				}
			}

		}
	}

	buildCmd := commandJoined
	for x := 0; x < len(tool.Parameters); x++ {
		if !validParams[x] {
			c.sendConfigMessageToChannel(channel, "invalid_parameter", tool.Parameters[x].Name)
			return false
		}
		re := regexp.MustCompile(`\${` + tool.Parameters[x].Name + `}`)
		if len(tool.Parameters[x].Match) > 0 {
			buildCmd = re.ReplaceAllString(buildCmd, strings.Join(cmds[x:], " "))
		} else {
			buildCmd = re.ReplaceAllString(buildCmd, cmds[x])
		}
	}
	buildCmd = fmt.Sprintf(
		"export TRIGGERED_AT=%s && export TRIGGERED_USER_ID=%s && export TRIGGERED_USER_NAME=%s && export TRIGGERED_CHANNEL_ID=%s && export TRIGGERED_CHANNEL_NAME=%s && cd %s && %s",
		timestamp,
		user,
		thisUser.Name,
		channel,
		strings.Join(c.getChannelNames([]string{channel}), ""),
		tool.Location,
		buildCmd,
	)
	splitOn := regexp.MustCompile(`\s\&\&`)
	displayCmd := splitOn.ReplaceAllString(buildCmd, " \\\n        &&")
	log.Info("Triggered Command:")
	log.Info(displayCmd)

	tmpCmd := []string{"bash", "-c", buildCmd}
	var rawOutput = c.runShellCommands(tmpCmd)
	// If the return string is more than 3500 characters, send it as a file
	var fileThreshold = 3500
	log.Info("Return length:")
	log.Info(len(rawOutput))
	var sendFile = false
	if len(rawOutput) > fileThreshold {
		sendFile = true
	}
	var retFile = fmt.Sprintf(" ----> Param Name:        %s\n", tool.Name)
	retFile += fmt.Sprintf(" ----> Param Description: %s\n", tool.Description)
	retFile += fmt.Sprintf(" ----> Param Log:         %s\n", strconv.FormatBool(tool.Log))
	retFile += fmt.Sprintf(" ----> Param Help:        %s\n", tool.Help)
	retFile += fmt.Sprintf(" ----> Param Trigger:     %s\n", tool.Trigger)
	retFile += fmt.Sprintf(" ----> Param Location:    %s\n", tool.Location)
	retFile += fmt.Sprintf(" ----> Param Command:     %s\n", commandJoined)
	retFile += fmt.Sprintf(" ----> Param Ephemeral:   %s\n", strconv.FormatBool(tool.Ephemeral))
	retFile += fmt.Sprintf(" ----> Param Response:    %s\n", tool.Response)
	retFile += fmt.Sprintf(" ----> Command:\n%s\n", displayCmd)
	retFile += rawOutput
	var ret = rawOutput
	switch tool.Response {
	case "file":
		sendFile = true
		ret = retFile
	case "code":
		ret = fmt.Sprintf("```%s```", rawOutput)
	}
	log.Debug(ret)
	if sendFile {
		var tFile = fmt.Sprintf("%s.txt", timestamp)
		log.Info(tFile)
		f, err := os.Create(tFile)
		if err != nil {
			log.Error(err)
		}
		defer f.Close()
		_, err2 := f.WriteString(ret)
		if err2 != nil {
			log.Error(err2)
		}
		uploadParams := slack.FileUploadParameters{
			Channels: []string{channel},
			File:     tFile,
		}

		if _, err := c.slackClient.UploadFile(uploadParams); err != nil {
			log.Errorf("Unexpected error uploading file: %s", err)
		}
	} else {
		if tool.Ephemeral {
			c.sendConfigMessageToChannel(channel, "ephemeral", "")
			c.SendMessageToUser(channel, user, ret)
		} else {
			c.SendMessageToChannel(channel, ret)
		}
	}
	if tool.Log {
		// c.logToChannel(channel, user, ret)

		var tFile = fmt.Sprintf("bashbot-log-%s.txt", timestamp)
		log.Info(tFile)
		f, err := os.Create(tFile)
		if err != nil {
			log.Error(err)
		}
		defer f.Close()
		_, err2 := f.WriteString(retFile)
		if err2 != nil {
			log.Error(err2)
		}
		uploadParams := slack.FileUploadParameters{
			Channels: []string{c.cfg.Admins[0].LogChannelId},
			File:     tFile,
		}

		if _, err := c.slackClient.UploadFile(uploadParams); err != nil {
			log.Errorf("Unexpected error uploading file: %s", err)
		}
	}
	return true
}

func (c *Client) logToChannel(channelID, userID, msg string) {
	user, err := c.slackClient.GetUserInfo(userID)
	if err != nil {
		log.Errorf("can't get user: %v", err)
		return
	}
	// Display message in chat-ops-log unless it came from admin channel
	if channelID == c.cfg.Admins[0].PrivateChannelId {
		return
	}
	channel := c.getChannelNames([]string{channelID})
	retacks := regexp.MustCompile("`")
	msg = retacks.ReplaceAllLiteralString(msg, "")
	msg = truncateString(msg, 1000)
	output := fmt.Sprintf("%s <@%s> <#%s> - %s", c.cfg.Admins[0].AppName, user.ID, channelID, msg)
	c.SendMessageToChannel(c.cfg.Admins[0].LogChannelId, output)
	log.Debugf("Bashbot command triggered channel: %s", channel)
	log.Info(output)
}

// ConfigureLogger configures the logger used by bashbot to set the log level
// and also the log format.
func ConfigureLogger(logLevel, logFormat string) {
	log.SetOutput(os.Stdout)

	switch logLevel {
	case "info":
		log.SetLevel(log.InfoLevel)
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	default:
		log.SetLevel(log.InfoLevel)
		log.Warn(fmt.Sprintf("Invalid log-level (setting to info level): %s", logLevel))
	}

	if logFormat == "json" {
		log.SetFormatter(&log.JSONFormatter{})
		return
	}
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})
}
