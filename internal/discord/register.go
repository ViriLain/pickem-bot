package discord

import (
	"fmt"

	"github.com/bwmarrin/discordgo"

	"github.com/ViriLain/pickem-bot/internal/commands"
)

var optionTypes = map[string]discordgo.ApplicationCommandOptionType{
	commands.TypeString:  discordgo.ApplicationCommandOptionString,
	commands.TypeInteger: discordgo.ApplicationCommandOptionInteger,
	commands.TypeBoolean: discordgo.ApplicationCommandOptionBoolean,
	commands.TypeUser:    discordgo.ApplicationCommandOptionUser,
	commands.TypeChannel: discordgo.ApplicationCommandOptionChannel,
}

// buildCommands turns command specs into guild slash commands. A spec with
// Sub becomes a command whose options are subcommands. Admin gating is done
// by role at dispatch time, so no default member permissions are set.
func buildCommands(specs []commands.Spec) ([]*discordgo.ApplicationCommand, error) {
	out := make([]*discordgo.ApplicationCommand, 0, len(specs))
	for _, s := range specs {
		cmd := &discordgo.ApplicationCommand{
			Type:        discordgo.ChatApplicationCommand,
			Name:        s.Name,
			Description: s.Description,
		}
		if len(s.Sub) > 0 {
			for _, sub := range s.Sub {
				opts, err := buildOptions(s.Name+" "+sub.Name, sub.Options)
				if err != nil {
					return nil, err
				}
				cmd.Options = append(cmd.Options, &discordgo.ApplicationCommandOption{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        sub.Name,
					Description: sub.Description,
					Options:     opts,
				})
			}
		} else {
			opts, err := buildOptions(s.Name, s.Options)
			if err != nil {
				return nil, err
			}
			cmd.Options = opts
		}
		out = append(out, cmd)
	}
	return out, nil
}

func buildOptions(path string, opts []commands.Option) ([]*discordgo.ApplicationCommandOption, error) {
	var out []*discordgo.ApplicationCommandOption
	for _, o := range opts {
		t, ok := optionTypes[o.Type]
		if !ok {
			return nil, fmt.Errorf("discord: /%s option %q has unknown type %q", path, o.Name, o.Type)
		}
		opt := &discordgo.ApplicationCommandOption{
			Type:        t,
			Name:        o.Name,
			Description: o.Description,
			Required:    o.Required,
		}
		if t == discordgo.ApplicationCommandOptionChannel {
			// Cards are plain messages; only text and announcement channels take them.
			opt.ChannelTypes = []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews}
		}
		for _, c := range optionChoices(path, o.Name) {
			opt.Choices = append(opt.Choices, &discordgo.ApplicationCommandOptionChoice{Name: c.Name, Value: c.Value})
		}
		out = append(out, opt)
	}
	return out, nil
}
