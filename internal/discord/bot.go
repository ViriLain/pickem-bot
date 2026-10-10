// Package discord is the thin adapter between Discord (via discordgo) and the
// card and commands packages. It posts and edits match cards, posts alerts
// and announcements, registers slash commands for one guild and routes
// interactions. It holds no game rules.
package discord

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"

	"github.com/ViriLain/pickem-bot/internal/card"
	"github.com/ViriLain/pickem-bot/internal/commands"
)

// Bot is a Discord session bound to one guild. It implements card.Poster
// and the scheduler's Alerter.
type Bot struct {
	session        *discordgo.Session
	guildID        string
	adminRoleID    string
	adminChannelID string

	mu     sync.Mutex
	cards  *card.Service
	cmds   *commands.Handlers
	remove func()
}

var _ card.Poster = (*Bot)(nil)

// New creates a Bot. It does not connect; Start does.
func New(token, guildID, adminRoleID, adminChannelID string) (*Bot, error) {
	var missing []string
	for _, f := range []struct{ name, val string }{
		{"token", token}, {"guild ID", guildID}, {"admin role ID", adminRoleID}, {"admin channel ID", adminChannelID},
	} {
		if strings.TrimSpace(f.val) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("discord: missing %s", strings.Join(missing, ", "))
	}
	token = strings.TrimPrefix(strings.TrimSpace(token), "Bot ")
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("discord: new session: %w", err)
	}
	// Interactions arrive whatever the intents; guilds is the minimum and
	// needs no privileged intent.
	s.Identify.Intents = discordgo.IntentsGuilds
	return &Bot{session: s, guildID: guildID, adminRoleID: adminRoleID, adminChannelID: adminChannelID}, nil
}

// noPings allows no mentions to notify anyone.
func noPings() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

// userPings lets user mentions notify those users, nothing else.
func userPings() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeUsers}}
}

// components turns card buttons into action rows. It never returns nil, so
// an edit always replaces the old buttons.
func components(buttons []card.Button) []discordgo.MessageComponent {
	out := []discordgo.MessageComponent{}
	for _, row := range buttonRows(buttons) {
		var comps []discordgo.MessageComponent
		for _, b := range row {
			btn := discordgo.Button{
				Label:    b.Label,
				Style:    discordgo.PrimaryButton,
				Disabled: b.Disabled,
				CustomID: b.ID,
			}
			if b.Emoji != "" {
				btn.Emoji = &discordgo.ComponentEmoji{Name: b.Emoji}
			}
			comps = append(comps, btn)
		}
		out = append(out, discordgo.ActionsRow{Components: comps})
	}
	return out
}

// Post sends a card and returns its message ID. Mentions in the card do not
// ping anyone.
func (b *Bot) Post(ctx context.Context, channelID string, v card.View) (string, error) {
	msg, err := b.session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         cardContent(v),
		Components:      components(v.Buttons),
		AllowedMentions: noPings(),
	}, discordgo.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("discord: post card in %s: %w", channelID, err)
	}
	return msg.ID, nil
}

// Edit replaces a posted card's text and buttons.
func (b *Bot) Edit(ctx context.Context, channelID, messageID string, v card.View) error {
	content := cardContent(v)
	comps := components(v.Buttons)
	edit := discordgo.NewMessageEdit(channelID, messageID)
	edit.Content = &content
	edit.Components = &comps
	edit.AllowedMentions = noPings()
	if _, err := b.session.ChannelMessageEditComplex(edit, discordgo.WithContext(ctx)); err != nil {
		return fmt.Errorf("discord: edit card %s in %s: %w", messageID, channelID, err)
	}
	return nil
}

// Announce posts plain text whose user mentions ping. Long text is split
// across messages.
func (b *Bot) Announce(ctx context.Context, channelID, text string) error {
	return b.send(ctx, channelID, text, userPings())
}

// Alert posts text to the admin channel without pinging anyone.
func (b *Bot) Alert(ctx context.Context, text string) error {
	return b.send(ctx, b.adminChannelID, text, noPings())
}

func (b *Bot) send(ctx context.Context, channelID, text string, mentions *discordgo.MessageAllowedMentions) error {
	for _, piece := range splitMessage(text, maxContent) {
		_, err := b.session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
			Content:         piece,
			AllowedMentions: mentions,
		}, discordgo.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("discord: send to %s: %w", channelID, err)
		}
	}
	return nil
}

// Start routes interactions to cards and cmds, opens the gateway session and
// registers commands.Specs() as guild commands, replacing any others.
func (b *Bot) Start(ctx context.Context, cards *card.Service, cmds *commands.Handlers) error {
	if cards == nil || cmds == nil {
		return errors.New("discord: Start needs cards and commands")
	}
	appCmds, err := buildCommands(commands.Specs())
	if err != nil {
		return err
	}

	b.mu.Lock()
	b.cards, b.cmds = cards, cmds
	if b.remove == nil {
		b.remove = b.session.AddHandler(b.onInteraction)
	}
	b.mu.Unlock()

	if err := b.session.Open(); err != nil {
		return fmt.Errorf("discord: open session: %w", err)
	}
	appID := b.applicationID()
	if appID == "" {
		_ = b.session.Close()
		return errors.New("discord: no application ID after connecting")
	}
	if _, err := b.session.ApplicationCommandBulkOverwrite(appID, b.guildID, appCmds, discordgo.WithContext(ctx)); err != nil {
		_ = b.session.Close()
		return fmt.Errorf("discord: register commands in guild %s: %w", b.guildID, err)
	}
	log.Printf("discord: connected, %d commands registered in guild %s", len(appCmds), b.guildID)
	return nil
}

// applicationID reads the application ID from the READY payload, falling
// back to the bot user ID, which is the same for current applications.
func (b *Bot) applicationID() string {
	st := b.session.State
	if st == nil {
		return ""
	}
	st.RLock()
	defer st.RUnlock()
	if st.Application != nil && st.Application.ID != "" {
		return st.Application.ID
	}
	if st.User != nil {
		return st.User.ID
	}
	return ""
}

// Close stops routing interactions and closes the gateway session.
func (b *Bot) Close() error {
	b.mu.Lock()
	if b.remove != nil {
		b.remove()
		b.remove = nil
	}
	b.mu.Unlock()
	return b.session.Close()
}

func (b *Bot) handlers() (*card.Service, *commands.Handlers) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cards, b.cmds
}

// interactionUserID returns who triggered an interaction: the member's user
// in a guild, the user in a DM.
func interactionUserID(i *discordgo.Interaction) string {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

// onInteraction runs in its own goroutine for each interaction.
func (b *Bot) onInteraction(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("discord: panic handling interaction: %v\n%s", r, debug.Stack())
		}
	}()
	if ic == nil || ic.Interaction == nil {
		return
	}
	i := ic.Interaction
	if i.GuildID != b.guildID {
		return
	}
	cards, cmds := b.handlers()
	if cards == nil || cmds == nil {
		return
	}
	switch i.Type {
	case discordgo.InteractionMessageComponent:
		b.handleComponent(s, i, cards)
	case discordgo.InteractionApplicationCommand:
		b.handleCommand(s, i, cmds)
	}
}

func (b *Bot) handleComponent(s *discordgo.Session, i *discordgo.Interaction, cards *card.Service) {
	customID := i.MessageComponentData().CustomID
	if !isPickButton(customID) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), interactionTimeout)
	defer cancel()
	// HandlePick also edits the card, which can take longer than the 3
	// seconds Discord allows for a first response, so acknowledge first.
	if err := deferResponse(ctx, s, i, true); err != nil {
		log.Printf("discord: defer pick %s: %v", customID, err)
		return
	}
	reply, err := cards.HandlePick(ctx, interactionUserID(i), customID)
	if err != nil {
		log.Printf("discord: pick %s by %s: %v", customID, interactionUserID(i), err)
	}
	editResponse(s, i, pickReply(reply, err))
}

func (b *Bot) handleCommand(s *discordgo.Session, i *discordgo.Interaction, cmds *commands.Handlers) {
	data := i.ApplicationCommandData()
	path, args := flattenCommand(data.Name, optionNodes(data.Options))
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout(path))
	defer cancel()
	if err := deferResponse(ctx, s, i, deferEphemeral(path)); err != nil {
		log.Printf("discord: defer /%s: %v", path, err)
		return
	}
	caller := commands.Caller{
		GuildID: i.GuildID,
		UserID:  interactionUserID(i),
		IsAdmin: i.Member != nil && hasRole(i.Member.Roles, b.adminRoleID),
	}
	reply := cmds.Dispatch(ctx, caller, path, args)
	editResponse(s, i, replyText(reply.Text))
}

// optionNodes copies discordgo's option tree into the Discord-free form.
func optionNodes(opts []*discordgo.ApplicationCommandInteractionDataOption) []optionNode {
	out := make([]optionNode, 0, len(opts))
	for _, o := range opts {
		if o == nil {
			continue
		}
		n := optionNode{Name: o.Name}
		switch o.Type {
		case discordgo.ApplicationCommandOptionSubCommand, discordgo.ApplicationCommandOptionSubCommandGroup:
			n.Sub = true
			n.Options = optionNodes(o.Options)
		default:
			n.Value = o.Value
		}
		out = append(out, n)
	}
	return out
}

// deferResponse acknowledges an interaction; the real reply follows with
// editResponse.
func deferResponse(ctx context.Context, s *discordgo.Session, i *discordgo.Interaction, ephemeral bool) error {
	resp := &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredChannelMessageWithSource}
	if ephemeral {
		resp.Data = &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral}
	}
	return s.InteractionRespond(i, resp, discordgo.WithContext(ctx))
}

// editResponse replaces the deferred response with text. Mentions in it do
// not ping. It uses its own deadline so a slow command still gets a reply.
func editResponse(s *discordgo.Session, i *discordgo.Interaction, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), editTimeout)
	defer cancel()
	if _, err := s.InteractionResponseEdit(i, &discordgo.WebhookEdit{
		Content:         &text,
		AllowedMentions: noPings(),
	}, discordgo.WithContext(ctx)); err != nil {
		log.Printf("discord: edit interaction response: %v", err)
	}
}
