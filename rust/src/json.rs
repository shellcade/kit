//! A minimal RFC 8259 JSON well-formedness scanner — the dependency-free
//! counterpart of Go's `json.Valid`, used to validate declared config-spec
//! schemas at meta() encode time (ABI.md §4.2). It checks syntax only; it
//! allocates nothing and bounds nesting so a pathological document cannot
//! blow the guest stack.

/// Nesting bound for arrays/objects. Schemas are shallow; 128 is generous.
const MAX_DEPTH: usize = 128;

/// Reports whether `s` is a single well-formed JSON value.
pub(crate) fn valid(s: &str) -> bool {
    let b = s.as_bytes();
    let mut p = Parser { b, i: 0 };
    p.ws();
    if !p.value(0) {
        return false;
    }
    p.ws();
    p.i == b.len()
}

struct Parser<'a> {
    b: &'a [u8],
    i: usize,
}

impl Parser<'_> {
    fn peek(&self) -> Option<u8> {
        self.b.get(self.i).copied()
    }

    fn ws(&mut self) {
        while matches!(self.peek(), Some(b' ' | b'\t' | b'\n' | b'\r')) {
            self.i += 1;
        }
    }

    fn eat(&mut self, c: u8) -> bool {
        if self.peek() == Some(c) {
            self.i += 1;
            true
        } else {
            false
        }
    }

    fn lit(&mut self, lit: &[u8]) -> bool {
        if self.b[self.i..].starts_with(lit) {
            self.i += lit.len();
            true
        } else {
            false
        }
    }

    fn value(&mut self, depth: usize) -> bool {
        if depth > MAX_DEPTH {
            return false;
        }
        match self.peek() {
            Some(b'{') => self.object(depth),
            Some(b'[') => self.array(depth),
            Some(b'"') => self.string(),
            Some(b't') => self.lit(b"true"),
            Some(b'f') => self.lit(b"false"),
            Some(b'n') => self.lit(b"null"),
            Some(b'-' | b'0'..=b'9') => self.number(),
            _ => false,
        }
    }

    fn object(&mut self, depth: usize) -> bool {
        self.i += 1; // '{'
        self.ws();
        if self.eat(b'}') {
            return true;
        }
        loop {
            self.ws();
            if !self.string() {
                return false;
            }
            self.ws();
            if !self.eat(b':') {
                return false;
            }
            self.ws();
            if !self.value(depth + 1) {
                return false;
            }
            self.ws();
            if self.eat(b'}') {
                return true;
            }
            if !self.eat(b',') {
                return false;
            }
        }
    }

    fn array(&mut self, depth: usize) -> bool {
        self.i += 1; // '['
        self.ws();
        if self.eat(b']') {
            return true;
        }
        loop {
            self.ws();
            if !self.value(depth + 1) {
                return false;
            }
            self.ws();
            if self.eat(b']') {
                return true;
            }
            if !self.eat(b',') {
                return false;
            }
        }
    }

    fn string(&mut self) -> bool {
        if !self.eat(b'"') {
            return false;
        }
        while let Some(c) = self.peek() {
            self.i += 1;
            match c {
                b'"' => return true,
                b'\\' => match self.peek() {
                    Some(b'"' | b'\\' | b'/' | b'b' | b'f' | b'n' | b'r' | b't') => self.i += 1,
                    Some(b'u') => {
                        self.i += 1;
                        for _ in 0..4 {
                            match self.peek() {
                                Some(h) if h.is_ascii_hexdigit() => self.i += 1,
                                _ => return false,
                            }
                        }
                    }
                    _ => return false,
                },
                // Raw control characters are forbidden inside strings. Any
                // other byte (incl. multi-byte UTF-8, valid by &str input)
                // passes through.
                0x00..=0x1f => return false,
                _ => {}
            }
        }
        false // unterminated
    }

    fn number(&mut self) -> bool {
        self.eat(b'-');
        // int part: 0, or 1-9 digits
        match self.peek() {
            Some(b'0') => self.i += 1,
            Some(b'1'..=b'9') => {
                while matches!(self.peek(), Some(b'0'..=b'9')) {
                    self.i += 1;
                }
            }
            _ => return false,
        }
        if self.eat(b'.') {
            if !matches!(self.peek(), Some(b'0'..=b'9')) {
                return false;
            }
            while matches!(self.peek(), Some(b'0'..=b'9')) {
                self.i += 1;
            }
        }
        if matches!(self.peek(), Some(b'e' | b'E')) {
            self.i += 1;
            if matches!(self.peek(), Some(b'+' | b'-')) {
                self.i += 1;
            }
            if !matches!(self.peek(), Some(b'0'..=b'9')) {
                return false;
            }
            while matches!(self.peek(), Some(b'0'..=b'9')) {
                self.i += 1;
            }
        }
        true
    }
}

#[cfg(test)]
mod tests {
    use super::valid;

    #[test]
    fn accepts_well_formed() {
        for s in [
            "{}",
            "[]",
            "null",
            "true",
            "-1.5e+10",
            r#""hi \"there\" é""#,
            r#"{"name":"Default","weights":{"7":1},"paytable":[{"faces":"7","multiplier":500}]}"#,
            "  [1, 2, {\"a\": [false]}]\n",
        ] {
            assert!(valid(s), "want valid: {s}");
        }
    }

    #[test]
    fn rejects_malformed() {
        for s in [
            "",
            "{",
            "{nope",
            "[1,]",
            "{\"a\":}",
            "{\"a\" 1}",
            "01",
            "1.",
            "1e",
            "\"unterminated",
            "\"bad \\x escape\"",
            "true false",
            "{\"a\":1} extra",
        ] {
            assert!(!valid(s), "want invalid: {s}");
        }
    }

    #[test]
    fn bounds_nesting() {
        let deep = "[".repeat(1000) + &"]".repeat(1000);
        assert!(!valid(&deep));
    }
}
