//! Input as a sum type: illegal states (a printable rune carrying a named key,
//! an unknown key value) are unrepresentable. The dispatch layer (`__rt`)
//! enforces the v2 tolerant-reader rule — an event whose kind or key this SDK
//! version does not recognise is silently dropped (no fault, no callback), so
//! future input growth (mouse, paste, focus, new keys) extends additively
//! without breaking built artifacts.

/// A named (non-printable) key. Values are wire-stable (ABI.md §2) and grow
/// additively; `#[non_exhaustive]` so a newer SDK can add keys without a major.
#[non_exhaustive]
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Key {
    Enter,
    Backspace,
    Esc,
    Tab,
    Up,
    Down,
    Left,
    Right,
    CtrlC,
}

impl Key {
    /// Wire value (ABI.md §2) → Key. `None` for KeyNone (0) and unknown values:
    /// both are dropped by dispatch per the tolerant-reader rule.
    pub(crate) fn from_wire(v: u8) -> Option<Key> {
        Some(match v {
            1 => Key::Enter,
            2 => Key::Backspace,
            3 => Key::Esc,
            4 => Key::Tab,
            5 => Key::Up,
            6 => Key::Down,
            7 => Key::Left,
            8 => Key::Right,
            9 => Key::CtrlC,
            _ => return None,
        })
    }
}

/// The SDK-neutral input event (Go's `kit.Input`, as a sum type).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Input {
    /// A printable rune.
    Char(char),
    /// A named key.
    Key(Key),
}

/// Selects how an [`Input`] is interpreted (mirrors Go `InputContext`).
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum InputContext {
    #[default]
    Nav,
    Command,
    Text,
}

/// A resolved, semantic input action.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Action {
    None,
    Up,
    Down,
    Left,
    Right,
    Confirm,
    Back,
}

impl Input {
    /// Map this input to a semantic [`Action`] for the given context — the
    /// platform's canonical control vocabulary (Go's free `kit.Resolve`):
    ///
    /// ```text
    /// Up=↑/k  Down=↓/j  Left=←/h  Right=→/l  Confirm=Enter/Space  Back=Esc/q/Ctrl-C
    /// ```
    pub fn resolve(self, ctx: InputContext) -> Action {
        if let Input::Key(k) = self {
            if k == Key::Esc || k == Key::CtrlC {
                return Action::Back;
            }
        }
        if ctx == InputContext::Text {
            return Action::None;
        }
        match self {
            Input::Key(k) => match k {
                Key::Up => Action::Up,
                Key::Down => Action::Down,
                Key::Left => Action::Left,
                Key::Right => Action::Right,
                Key::Enter => Action::Confirm,
                _ => Action::None,
            },
            Input::Char(r) => match r {
                ' ' => Action::Confirm,
                'q' => Action::Back,
                'k' if ctx == InputContext::Nav => Action::Up,
                'j' if ctx == InputContext::Nav => Action::Down,
                'h' if ctx == InputContext::Nav => Action::Left,
                'l' if ctx == InputContext::Nav => Action::Right,
                _ => Action::None,
            },
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn resolve_matches_go_vocabulary() {
        use InputContext::*;
        assert_eq!(Input::Key(Key::Esc).resolve(Text), Action::Back);
        assert_eq!(Input::Key(Key::CtrlC).resolve(Nav), Action::Back);
        assert_eq!(Input::Char('q').resolve(Nav), Action::Back);
        assert_eq!(Input::Char('q').resolve(Text), Action::None); // text mode types 'q'
        assert_eq!(Input::Char(' ').resolve(Command), Action::Confirm);
        assert_eq!(Input::Key(Key::Enter).resolve(Nav), Action::Confirm);
        assert_eq!(Input::Char('k').resolve(Nav), Action::Up);
        assert_eq!(Input::Char('k').resolve(Command), Action::None); // hjkl is Nav-only
        assert_eq!(Input::Key(Key::Left).resolve(Command), Action::Left);
    }

    #[test]
    fn unknown_wire_keys_are_none() {
        assert_eq!(Key::from_wire(0), None); // KeyNone
        assert_eq!(Key::from_wire(10), None); // future key
        assert_eq!(Key::from_wire(5), Some(Key::Up));
    }
}
