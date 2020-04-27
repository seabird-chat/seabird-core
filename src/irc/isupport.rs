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

    pub fn get_prefix_map(&self) -> Option<BTreeMap<u8, u8>> {
        // Sample: (qaohv)~&@%+
        self.get("PREFIX").and_then(|val| {
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

/*

// IsEnabled will check for boolean ISupport values
func (t *ISupportTracker) IsEnabled(key string) bool {
    t.RLock()
    defer t.RUnlock()

    _, ok := t.data[key]
    return ok
}

// GetList will check for list ISupport values
func (t *ISupportTracker) GetList(key string) ([]string, bool) {
    t.RLock()
    defer t.RUnlock()

    data, ok := t.data[key]
    if !ok {
        return nil, false
    }

    return strings.Split(data, ","), true
}

// GetMap will check for map ISupport values
func (t *ISupportTracker) GetMap(key string) (map[string]string, bool) {
    t.RLock()
    defer t.RUnlock()

    data, ok := t.data[key]
    if !ok {
        return nil, false
    }

    ret := make(map[string]string)

    for _, v := range strings.Split(data, ",") {
        innerData := strings.SplitN(v, ":", 2)
        if len(innerData) != 2 {
            return nil, false
        }

        ret[innerData[0]] = innerData[1]
    }

    return ret, true
}

// GetRaw will get the raw ISupport values
func (t *ISupportTracker) GetRaw(key string) (string, bool) {
    t.RLock()
    defer t.RUnlock()

    ret, ok := t.data[key]
    return ret, ok
}
*/
