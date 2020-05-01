use std::iter::FromIterator;

use crate::prelude::*;

#[derive(Debug)]
pub struct ISupportTracker {
    data: BTreeMap<String, String>,
}

impl ISupportTracker {
    pub fn new() -> Self {
        let mut data = BTreeMap::new();
        data.insert("PREFIX".to_string(), "(ov)@+".to_string());

        ISupportTracker { data: data }
    }

    pub fn handle_message(&mut self, msg: &irc::Message) {
        if msg.command != "005" {
            return;
        }

        if msg.params.len() < 2 {
            // Malformed ISupport message
            return;
        }

        // Check for really old servers (or servers which based 005 off of rfc2812.
        if !msg.params[1].ends_with("server") {
            // This server doesn't appear to support ISupport messages. Here there be dragons.
            return;
        }

        for param in msg.params[1..].as_ref() {
            let mut data = param.splitn(2, '=');
            match (data.next(), data.next()) {
                (Some(key), Some(val)) => {
                    self.data.insert(key.to_string(), val.to_string());
                }
                (Some(key), None) => {
                    self.data.insert(key.to_string(), "".to_string());
                }
                (None, _) => {
                    unreachable!();
                }
            }
        }
    }

    #[allow(dead_code)]
    pub fn get(&self, key: &str) -> Option<String> {
        self.data.get(key).map(|val| val.clone())
    }

    #[allow(dead_code)]
    pub fn is_enabled(&self, key: &str) -> bool {
        self.data.contains_key(key)
    }

    #[allow(dead_code)]
    pub fn get_list(&self, key: &str) -> Option<Vec<String>> {
        self.data
            .get(key)
            .map(|val| val.split(',').map(String::from).collect())
    }

    #[allow(dead_code)]
    pub fn get_map(&self, key: &str) -> Option<BTreeMap<String, String>> {
        self.data.get(key).and_then(|val| {
            let inner = val.split(',');

            FromIterator::from_iter(inner.map(|inner| {
                let mut split = inner.splitn(2, ':');
                match (split.next(), split.next()) {
                    (Some(k), Some(v)) => Some((k.to_string(), v.to_string())),
                    _ => None,
                }
            }))
        })
    }

    pub fn get_prefix_map(&self) -> Option<BTreeMap<u8, u8>> {
        // Sample: (qaohv)~&@%+
        self.data.get("PREFIX").and_then(|val| {
            // We only care about the symbols
            let idx = val.find(')');
            if !val.starts_with('(') || idx.is_none() {
                return None;
            }
            let idx = idx.unwrap();

            let modes = &val[1..idx];
            let symbols = &val[idx + 1..];

            if modes.len() != symbols.len() {
                // "Mismatched modes and symbols"
                return None;
            }

            Some(BTreeMap::from_iter(symbols.bytes().zip(modes.bytes())))
        })
    }
}
