#!/opt/blinka/bin/python

import board
import digitalio

import adafruit_aw9523

i2c = board.I2C()  # uses board.SCL and board.SDA
# i2c = board.STEMMA_I2C()  # For using the built-in STEMMA QT connector on a microcontroller
aw = adafruit_aw9523.AW9523(i2c)

led_pin = aw.get_pin(1)  # LED on AW9523 io 1
button_pin = aw.get_pin(0)  # Button on AW io 0

# LED is an output, initialize to high
led_pin.switch_to_output(value=True)
# Button is an input, note pull-ups are not supported!
button_pin.direction = digitalio.Direction.INPUT

while True:
    # LED mirrors button pin
    led_pin.value = button_pin.value

