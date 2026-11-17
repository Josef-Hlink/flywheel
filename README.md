# flywheel

_created on: 03-11-2026_

## Description

Ride your smart trainer from the terminal: no app, no account, no virtual
world. flywheel speaks the Bluetooth Fitness Machine Service (FTMS) that most
current smart trainers support. It sets an ERG target or a resistance level,
shows your power, cadence and speed as you ride, and saves every ride to a
local SQLite database.

## Example

`flywheel train` connects, then prints a line per second while a prompt takes
new targets:

```
$ flywheel train
connected to KICKR CORE 7146; disconnects after 5m0s without pedalling
  250w     ERG: hold 250 W
  30%      resistance mode at 30%
  220      new target in the current mode (ERG if none yet)
  +10 -5   nudge the current target
  s        the ride so far
  q        leave (or ctrl-d)
   -:--  none set            0 W     0 rpm    0.0 km/h
> 300
set ERG 300 W
   0:00  ERG 300 W           1 W     0 rpm    0.0 km/h
   0:01  ERG 300 W          40 W     0 rpm    0.5 km/h
   0:02  ERG 300 W         163 W     0 rpm    3.4 km/h
   0:03  ERG 300 W         243 W    64 rpm    8.0 km/h
   0:04  ERG 300 W         305 W    78 rpm   11.5 km/h
   0:05  ERG 300 W         302 W    90 rpm   14.7 km/h
   0:06  ERG 300 W         301 W    94 rpm   17.2 km/h
   0:07  ERG 300 W         300 W    96 rpm   19.6 km/h
   0:08  ERG 300 W         301 W    97 rpm   21.4 km/h
   0:09  ERG 300 W         301 W    97 rpm   22.9 km/h
   0:10  ERG 300 W         300 W    98 rpm   24.4 km/h
   0:11  ERG 300 W         299 W    98 rpm   25.6 km/h
   0:12  ERG 300 W         299 W    99 rpm   26.9 km/h
   0:13  ERG 300 W         300 W    99 rpm   27.9 km/h
   0:14  ERG 300 W         300 W    98 rpm   28.9 km/h
   0:15  ERG 300 W         300 W    98 rpm   29.8 km/h
   0:16  ERG 300 W         300 W    97 rpm   30.6 km/h
   0:17  ERG 300 W         300 W    97 rpm   31.3 km/h
   0:18  ERG 300 W         225 W    97 rpm   32.0 km/h
   0:18  ERG 300 W           0 W    97 rpm   32.3 km/h
> 200
set ERG 200 W
   0:19  ERG 200 W          18 W     0 rpm   17.5 km/h
   0:20  ERG 200 W         101 W     0 rpm   17.4 km/h
   0:21  ERG 200 W         170 W    72 rpm   17.9 km/h
   0:22  ERG 200 W         203 W    97 rpm   19.0 km/h
   0:23  ERG 200 W         201 W   106 rpm   20.2 km/h
   0:24  ERG 200 W         201 W   111 rpm   21.2 km/h
> q
disconnected from KICKR CORE 7146
saved ride 2: 0:24 (0:52 elapsed), avg 232 W, max 305 W, 6 kJ, 93 rpm, 0.3 km
```

The clock is moving time and pauses while you're off the bike. A stop prints
a single line rather than one per second.

## Install

```bash
brew install Josef-Hlink/tap/flywheel
```

With nix:

```bash
nix profile install github:Josef-Hlink/nur-packages#flywheel
```

Or with Go. On macOS this needs cgo, for CoreBluetooth, so Xcode's command
line tools must be installed:

```bash
go install github.com/Josef-Hlink/flywheel/cmd/flywheel@latest
```

The first time flywheel scans, macOS asks whether your terminal may use
Bluetooth.

## Usage

```bash
flywheel scan          # list nearby FTMS trainers
flywheel train         # ride at a prompt (connects, and disconnects when you leave)
flywheel rides         # your latest rides
flywheel weight 72     # your weight in kg, for road speed (default 75)
```

At the `train` prompt:

| input     | does                                                |
| --------- | --------------------------------------------------- |
| `250w`    | ERG: hold 250 W whatever your cadence               |
| `30%`     | resistance mode at 30%                              |
| `220`     | a new target in the current mode (ERG if none yet)  |
| `+10` `-5` | nudge the current target                           |
| `s`       | the ride so far                                     |
| `q`       | leave (or ctrl-d)                                   |

The same controls work from separate commands, which suits scripts or a
second terminal:

```bash
flywheel connect       # connect in the background
flywheel set 250W      # ERG target
flywheel set 30%       # resistance level
flywheel status -w     # one live line, with peak power
flywheel disconnect    # release the trainer
```

### Buttons: Zwift Click v2

With `-click`, `train` and `connect` also take target changes from a Zwift
Click v2, so you can change the effort without reaching for the keyboard:

```bash
flywheel train -click
```

Press a button on the pod with A, B, Y and Z to wake it; flywheel connects
to it within a few seconds, and again whenever it has dozed off.

| button | ERG                         | resistance mode |
| ------ | --------------------------- | --------------- |
| `Y`    | +10 W                       | +1%             |
| `B`    | −10 W                       | −1%             |
| `A`    | +50 W                       | +5%             |
| `Z`    | −50 W                       | −5%             |
| `+`    | switch to resistance mode   | switch to ERG   |

The four face buttons sit in a diamond: up and down nudge, right and left
take bigger steps. `+` goes back to where the other mode was left. With no target yet, any
button starts ERG at 100 W. The buttons never take ERG past 500 W
(`-click-max` changes that); a target you type isn't held to it.

Only that pod works. The one with the arrows stays locked until the Zwift
app has unlocked it, so flywheel leaves it alone. The pods speak Zwift's own
undocumented protocol, worked out by
[BikeControl](https://github.com/jonasbark/swiftcontrol); a firmware update
may break it.

## How it works

**The connection.** `connect` starts a background process that holds the
Bluetooth link and takes commands over a unix socket. A trainer only follows
targets while it's connected, and connecting takes seconds, so the link has to
outlive single commands. The process releases the trainer and exits after
5 minutes without pedalling (`-idle` changes that).

**Rides.** Each connection records at most one ride, from your first pedal
stroke to your last, pauses included. A ride is kept once it has 10 seconds of
pedalling. It goes into `~/.local/share/flywheel/rides.db` (or under
`$XDG_DATA_HOME`): a summary in `rides`, and every sample in `samples` with
the target at that moment. Writes happen every 5 seconds, so a crash loses
seconds rather than the ride.

```bash
sqlite3 ~/.local/share/flywheel/rides.db 'SELECT * FROM rides'
```

**Speed.** A trainer's own speed only tells you how fast its flywheel spins.
With one fixed gear that's simply your cadence times a constant, whatever the
power. So, as riding apps do, flywheel works out speed and distance from your
power instead: on a flat road without wind, for your weight plus a 9 kg bike,
riding on the hoods on smooth tarmac. At 75 kg that settles at about 26 km/h
for 100 W, 34 km/h for 200 W and 40 km/h for 300 W, with realistic
acceleration and coasting in between.

**Safety.** A new target takes effect at once, under you. Disconnecting, or
the idle timeout, resets the trainer, so it never stays stuck in ERG mode.

## Compatibility

Tested with a Wahoo KICKR CORE and a Zwift Click v2 on macOS (Apple Silicon)
and on Linux (NixOS, x86-64). Other FTMS trainers should work. On Linux
flywheel talks to BlueZ, so the `bluetooth` service has to be running.

## License

MIT
