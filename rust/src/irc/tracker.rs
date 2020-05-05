use std::collections::BTreeSet;

use crate::irc::ISupportTracker;
use crate::prelude::*;

#[derive(Debug)]
pub struct Tracker {
    isupport: ISupportTracker,
    current_nick: Option<String>,
    channels: BTreeMap<String, ChannelState>,
}

#[derive(Debug, Clone)]
pub struct ChannelState {
    pub name: String,
    pub topic: Option<String>,
    pub users: BTreeSet<String>,
}

impl ChannelState {
    fn new(name: String) -> Self {
        ChannelState {
            name,
            topic: None,
            users: BTreeSet::new(),
        }
    }
}

impl Tracker {
    pub fn new() -> Self {
        Tracker {
            isupport: ISupportTracker::new(),
            current_nick: None,
            channels: BTreeMap::new(),
        }
    }

    pub fn get_current_nick(&self) -> Option<String> {
        self.current_nick.clone()
    }

    pub fn get_channel(&self, name: &str) -> Option<ChannelState> {
        self.channels.get(name).map(|val| val.clone())
    }

    pub fn list_channels(&self) -> Vec<String> {
        self.channels.keys().map(String::clone).collect()
    }

    pub fn handle_message(&mut self, msg: &irc::Message) {
        self.isupport.handle_message(msg);

        match (msg.command.as_str(), msg.params.len(), &msg.prefix) {
            ("001", 2, _) => self.handle_rpl_welcome(msg.params[0].as_str()),
            ("331", 3, _) => self.handle_rpl_no_topic(msg.params[1].as_str()),
            ("332", 3, _) => self.handle_rpl_topic(msg.params[1].as_str(), msg.params[2].as_str()),
            ("353", 4, _) => self
                .handle_rpl_nam_reply(msg.params[2].as_str(), msg.params[3].split(" ").collect()),
            ("JOIN", 1, Some(prefix)) => {
                self.handle_join(prefix.nick.as_str(), msg.params[0].as_str())
            }
            ("KICK", 3, _) => self.handle_kick(msg.params[1].as_str(), msg.params[0].as_str()),
            ("NICK", 1, Some(prefix)) => {
                self.handle_nick(prefix.nick.as_str(), msg.params[0].as_str())
            }
            ("PART", 2, Some(prefix)) => {
                self.handle_part(prefix.nick.as_str(), msg.params[0].as_str())
            }
            ("QUIT", 1, Some(prefix)) => self.handle_quit(prefix.nick.as_str()),
            ("TOPIC", 2, _) => self.handle_topic(msg.params[0].as_str(), msg.params[1].as_str()),

            _ => (),
        }
    }
}

impl Tracker {
    fn handle_rpl_welcome(&mut self, nick: &str) {
        self.current_nick = Some(nick.to_string());
    }

    fn handle_rpl_no_topic(&mut self, channel_name: &str) {
        if let Some(channel) = self.channels.get_mut(channel_name) {
            channel.topic = None;
        }
    }

    fn handle_rpl_topic(&mut self, channel_name: &str, topic: &str) {
        if let Some(channel) = self.channels.get_mut(channel_name) {
            channel.topic = Some(topic.to_string());
        }
    }

    fn handle_rpl_nam_reply(&mut self, channel_name: &str, users: Vec<&str>) {
        let prefix_map = self
            .isupport
            .get_prefix_map()
            .unwrap_or_else(|| BTreeMap::new());

        // The bot user should be added via JOIN so we need to filter it out. We
        // also use this chance to remove the unwanted prefixes.
        let users: Vec<_> = users
            .into_iter()
            .filter_map(|user| {
                if user.is_empty() || Some(user) == self.current_nick.as_deref() {
                    return None;
                }

                if prefix_map.contains_key(&user.as_bytes()[0]) {
                    Some(user[1..].to_string())
                } else {
                    Some(user.to_string())
                }
            })
            .collect();

        if let Some(channel) = self.channels.get_mut(channel_name) {
            for user in users.into_iter() {
                channel.users.insert(user.to_string());
            }
        } else {
            // Got RPL_NAMREPLY message for untracked channel
        }
    }

    fn handle_join(&mut self, nick: &str, channel_name: &str) {
        if !self.channels.contains_key(channel_name) && Some(nick) != self.current_nick.as_deref() {
            // Got JOIN message for untracked channel
            return;
        }

        let channel = self
            .channels
            .entry(channel_name.to_string())
            .or_insert_with(|| ChannelState::new(channel_name.to_string()));

        channel.users.insert(nick.to_string());
    }

    fn handle_kick(&mut self, nick: &str, channel_name: &str) {
        if !self.channels.contains_key(channel_name) {
            // Got KICK message for untracked channel
            return;
        }

        if Some(nick) == self.current_nick.as_deref() {
            self.channels.remove(channel_name);
        } else if let Some(channel) = self.channels.get_mut(channel_name) {
            channel.users.remove(nick);
        } else {
            unreachable!();
        }
    }

    fn handle_nick(&mut self, old_nick: &str, new_nick: &str) {
        for (_, channel) in self.channels.iter_mut() {
            if channel.users.remove(old_nick) {
                channel.users.insert(new_nick.to_string());
            }
        }
    }

    fn handle_part(&mut self, nick: &str, channel_name: &str) {
        if !self.channels.contains_key(channel_name) {
            // Got PART message for untracked channel
            return;
        }

        if Some(nick) == self.current_nick.as_deref() {
            self.channels.remove(channel_name);
        } else if let Some(channel) = self.channels.get_mut(channel_name) {
            channel.users.remove(nick);
        } else {
            unreachable!();
        }
    }

    fn handle_quit(&mut self, nick: &str) {
        for (_, channel) in self.channels.iter_mut() {
            channel.users.remove(nick);
        }
    }

    fn handle_topic(&mut self, channel_name: &str, topic: &str) {
        if let Some(channel) = self.channels.get_mut(channel_name) {
            channel.topic = Some(topic.to_string());
        }
    }
}
