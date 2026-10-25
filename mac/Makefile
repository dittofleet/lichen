VERSION ?= dev
APP = dist/Lichen.app
BUILD = swift build -c release

app:
	$(BUILD)
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS $(APP)/Contents/Resources
	cp ../assets/icon.icns $(APP)/Contents/Resources/AppIcon.icns
	cp "$$($(BUILD) --show-bin-path)/Lichen" $(APP)/Contents/MacOS/Lichen
	sed 's/VERSION/$(VERSION:v%=%)/' Info.plist > $(APP)/Contents/Info.plist
	codesign --force --sign - $(APP)
	codesign --verify --strict $(APP)

# Installs this build into /Applications. It drives the lichen command,
# which has to be installed already.
install: app
	rm -rf /Applications/Lichen.app
	ditto $(APP) /Applications/Lichen.app

test:
	swift test

clean:
	rm -rf .build dist

.PHONY: app install test clean
