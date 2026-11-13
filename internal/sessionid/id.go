// Package sessionid provides memorable session identities, not authentication secrets.
package sessionid

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const Pattern = `^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`

var pattern = regexp.MustCompile(Pattern)

func Valid(id string) bool { return pattern.MatchString(id) }

var words = strings.Fields(`
able absent acorn active actor actual adapter address
adept admiral adult adventure advice affair age agency
agile air airfield airport aisle album alcove alert
algebra alive alley almond alphabet alpine altitude amber
ample amulet anchor ancient angle animal annual answer
antelope antenna antique anvil apartment apple apron apt
arcade arch arctic arena arid armor army arrow
artist ascent ash athlete atlas attic audio autumn
avenue avocado award aware axle azure baby backpack
bacon badge bagel bakery balcony ball ballet balloon
ballroom bamboo banana band banjo bank banner barley
barn barrel baseball basic basil basin basket bass
bath battery bay bazaar beach beacon bead beam
bean beaver bed bedroom beige bell bench berry
bicycle bike bill birch bird birthday biscuit bison
bit black blade blank blanket blazer block bloom
blossom board boat body bolt bond bone bonnet
bonus book boots border boss bottle boulder boundary
bow bowl box boy bracket braid brain branch
brass brave bread breakfast breath breeze brew brick
bridge brief bright brisk broad bronze brook broom
brother brown brush bubble bucket buckle bud budget
buffalo bugle building bun bungalow bunker bunny burger
burgundy bus bush busy butter button cabin cable
cactus cafe cake calendar calf calm camel camera
camp canal candid candle candy canoe canvas canyon
cap cape capital capsule captain car carbon careful
cargo carpet carrot cart carton case cash castle
casual cave cedar ceiling cello cement center cereal
certain chair chalk chamber champion channel chapter charm
chart cheerful cheese cheetah chef cherry chess chest
chick child chime chimney chin chipmunk chisel chocolate
choir chord circle citrus city civil clam classic
clay clean clear clever cliff clock close closet
cloth cloud cloudy clover club coach coast cobalt
cocoa coconut code coffee coin cold collar colony
color column comedy comet comfy command company compass
concert cone cookie copper coral cord corn corner
cosmic cotton couch country county court cousin cove
cozy crab cracker craft crane cream credit creek
crest crew cricket crisp crop crown crust crystal
cub cubby cube cup cupboard curly curtain curve
cushion cyan cycle daily daisy dale dam dancer
daring dash date dawn day dear decent deck
decor deep deer deft delta den dense dentist
depot desert desk dessert dial diamond dice diner
dingo dinner direct dish disk divine dizzy dock
doctor dog doll dollar dolphin dome donkey door
dot double dough dove dragon drawer dreamy dress
driver droplet drum dry dual duck dune dust
duty dwarf eager eagle ear early earth east
easy echo edge editor elbow elder electric elegant
elk elm ember emerald engine equal eraser essay
estate even evening event exact exam exit expert
extra eye face factory fair faithful falcon family
fan fancy far farm farmer fast feast feather
fence fern ferry festival festive fiber fiddle field
fierce fig film final finch fine finger fire
firm first fish fit flag flake flame flash
flat flax fleet flint floor floral flour flower
fluent flute fly foam focal fog folder fond
food foot football forehead forest forge fork form
formal fortune fossil fountain fox frame frank free
fresh friend friendly frog front frost fruit fuel
full funny fur galaxy game garage garden garlic
gas gate gazelle gear gecko gem gentle giant
gift ginger giraffe girl glacier glad glass globe
glove glow goal goat gold golden good goose
gorilla gown graceful grade grain gram grand granite
grape graph grass gravel great green grey grid
grin grocery ground group grove guard guest guide
guitar gull gum gym habit hall hammer hand
handle hanger happy harbor hardy hare harp harvest
hasty hat haven hawk hay hazel hazy head
heap heart hearty heat heavy hedge helmet help
helpful hen hero heron hidden hill hive hobby
hockey holder hollow holly home honest honey hood
hook hoop hop horizon horn horse hotel hour
house hub hug hull humble ice iceberg icy
idea ideal idle igloo image inch index indigo
ink inlet inner insect iron island ivory ivy
jack jacket jade jam jar jasmine jeans jelly
jet jewel job jolly joyful juniper keen kettle
key khaki kind kite kiwi koala lake lamp
lantern larch large lark laser last late laurel
leaf lean left legal lemon leopard level light
likely lilac lily lime linen lion lively local
lone long lotus loud loyal lucky lunar lush
magenta magic major maple marble marsh mauve meadow
mellow melon merry meteor mild minor mint mirror
mist modern modest moist moon moose moss mountain
mouse moving mug mushroom mute narrow native natural
naval navy nearby neat nectar nest nettle new
next nice night nimble noble north novel nut
ocean ochre odd olive onyx open orange orbit
orchid otter outer oval owl oxford oyster paddle
pale palm panda paper patient peaceful peach pearl
pebble pecan pelican pepper petal petite pigeon pine
pink plain planet playful plum plush pocket polar
polite pond poplar poppy potato prime prism proud
puffin pumpkin pure purple quartz quick quiet quill
rabbit raccoon radish rain rainbow rapid rare raven
ready real red reed reef regal ribbon rice
rich ridge right ripe river robin rock rocket
rocky rose rosy round rowan royal ruby rugged
rural rustic safe sage sail salmon salt sand
sandy satin savvy scarf scarlet scenic seal second
secret secure seed select sepia serene shadow shady
sharp shell shield shiny shore short shy silent
silk silly silver simple single sky slate sleek
slim slow small smart smooth snow snowy snug
soap sock soft solar solid solo sound south
spare sparrow spear speedy spice spicy spider splendid
spoon spring spruce square stable star steady steep
stellar still stone stork storm stout straw stream
strong subtle sugar summit sun sunny super sure
swallow swan sweetcorn swift sword table tall talon
tame tan tasty tea teal temple tender thick
thin thistle thorn thread thunder thyme tidy tiger
timber tiny toast tomato topaz torch total tough
tower trail tree trout true trusty tulip turtle
twin ultra unique united upper urban useful valid
valley vast velvet violet vivid vocal wagon walnut
warm wary water watery wave wavy waxy weary
weekly west wet wheat wheel white whole wide
wild willing willow wind window windy wing winter
wise witty wolf wood woody worthy wren yacht
yak yarn yellow yew young zebra zesty zinnia
`)

// Memorable reports whether id is a word-pair name.
func Memorable(id string) bool {
	return regexp.MustCompile(`^[a-z]{3,12}-[a-z]{3,12}$`).MatchString(id)
}

// Random returns a word-pair name without reserving it; callers that create a
// directory for it must create it exclusively.
func Random() (string, error) {
	for {
		a, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
		if err != nil {
			return "", err
		}
		b, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
		if err != nil {
			return "", err
		}
		if a.Int64() != b.Int64() {
			return words[a.Int64()] + "-" + words[b.Int64()], nil
		}
	}
}

// Allocate reserves names across local processes and keeps retired names reserved.
// Remote launch journals additionally reject mismatched device/launch identities.
func Allocate(home string) (string, error) {
	directory := filepath.Join(home, ".config/fennec/session-names")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	for attempt := 0; attempt < 4096; attempt++ {
		a, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
		if err != nil {
			return "", err
		}
		b, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
		if err != nil {
			return "", err
		}
		if a.Int64() == b.Int64() {
			continue
		}
		id := words[a.Int64()] + "-" + words[b.Int64()]
		// Imported receipts and older journals also reserve their names.
		if _, err := os.Stat(filepath.Join(home, ".config/fennec/launches", id+".json")); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		file, err := os.OpenFile(filepath.Join(directory, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err = file.Close(); err != nil {
			return "", err
		}
		return id, nil
	}
	return "", fmt.Errorf("session name allocation exhausted; no existing session was reused")
}
