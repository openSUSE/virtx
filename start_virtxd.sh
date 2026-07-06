#! /bin/bash

sudo setpriv --reuid qemu --regid qemu --groups disk,sanlock --inh-caps=-all -- nohup ./virtxd &
