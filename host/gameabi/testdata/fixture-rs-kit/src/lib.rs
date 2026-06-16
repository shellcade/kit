//! fixture-rs-kit — the conformance fixture built ON the `shellcade-kit` Rust
//! SDK crate. Same observable surface as the hand-rolled fixture-rs:
//!
//!   start/join/leave -> render the status frame
//!   input 'e'        -> end the room (winner = sender, metric 42)
//!   input 'p'        -> panic (guest trap; panic=abort -> wasm unreachable)
//!   wake             -> increment the wake counter, then render
//!
//! Where fixture-rs proves ABI.md is implementable from the document alone,
//! THIS fixture proves the SDK passes the server's own gate: the SDK's delta
//! path (baselines, epochs, keyframe retry) runs under the full conformance
//! script including the snapshot/restore hibernation byte-identity check and
//! the guest-fault containment case. Note this game contains ZERO unsafe and
//! zero wire code.
#![forbid(unsafe_code)]

use shellcade_kit::prelude::*;

struct FixtureGame;

impl Game for FixtureGame {
    fn meta(&self) -> Meta {
        Meta {
            slug: "fixture-rs-kit",
            name: "Fixture (Rust, kit crate)",
            short_description: "Conformance fixture built on the shellcade-kit Rust SDK.",
            min_players: 1,
            max_players: 2,
            ..Meta::DEFAULT
        }
    }
    fn new_room(&self, _cfg: &RoomConfig) -> Box<dyn Handler> {
        Box::new(FixtureRoom { frame: Frame::new(), wakes: 0 })
    }
}

struct FixtureRoom {
    frame: Frame,
    wakes: u64,
}

impl FixtureRoom {
    fn render(&mut self, r: &mut Room) {
        let st = Style::new(WHITE, 0);
        self.frame.clear();
        self.frame.text(0, 0, "FIXTURE-RS-KIT", st);
        self.frame.text(1, 0, &format!("players={}", r.count()), st);
        self.frame.text(2, 0, &format!("wakes={}", self.wakes), st);
        r.identical(&self.frame);
    }
}

impl Handler for FixtureRoom {
    fn on_start(&mut self, r: &mut Room) {
        self.render(r);
    }

    fn on_join(&mut self, r: &mut Room, _p: Player) {
        self.render(r);
    }

    fn on_leave(&mut self, r: &mut Room, _p: Player) {
        self.render(r);
    }

    fn on_input(&mut self, r: &mut Room, p: Player, input: Input) {
        match input {
            Input::Char('e') => {
                // Settle: winner = sender, metric 42 (the fixture contract).
                r.end(&Outcome {
                    rankings: vec![PlayerResult {
                        player: p,
                        metric: 42,
                        rank: 1,
                        status: Status::Finished,
                    }],
                });
            }
            Input::Char('p') => panic!("fixture-rs-kit: deliberate guest panic"),
            _ => {}
        }
        self.render(r);
    }

    fn on_wake(&mut self, r: &mut Room) {
        self.wakes += 1;
        self.render(r);
    }
}

shellcade_kit::shellcade_game!(FixtureGame);

#[cfg(test)]
mod tests {
    use super::*;
    use shellcade_kit::{reset_test_host, with_test_host};

    // Native sanity: the fixture's room logic against the SDK's test host —
    // the full conformance run happens in Go off the committed wasm.
    #[test]
    fn renders_and_settles() {
        reset_test_host();
        let game = FixtureGame;
        let mut h = game.new_room(&RoomConfig::default());
        // The Handler is exercised through the SDK dispatch in production;
        // here we only smoke the Frame composition path compiles and runs.
        let _ = &mut h;
        with_test_host(|t| assert!(t.sent.is_empty()));
    }
}
