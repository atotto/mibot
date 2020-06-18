

## Video stream

test src

    $ gst-launch-1.0 videotestsrc ! 'video/x-raw, width=320, height=240' ! videoconvert ! video/x-raw,format=I420 ! vp8enc error-resilient=partitions keyframe-max-dist=10 auto-alt-ref=true cpu-used=5 deadline=1 ! rtpvp8pay ! udpsink host=127.0.0.1 port=5004

mjpeg http src

    $ gst-launch-1.0 souphttpsrc location=http://127.0.0.1:8080/mjpeg is-live=true ! multipartdemux ! image/jpeg,width=640,height=320,framerate=10/1 ! jpegdec ! videorate ! video/x-raw,framerate=5/1 ! videoconvert ! video/x-raw,format=I420 ! vp8enc error-resilient=partitions keyframe-max-dist=10 auto-alt-ref=true cpu-used=5 deadline=1 ! rtpvp8pay ! udpsink host=127.0.0.1 port=5004

uvc camera src

    $ gst-launch-1.0 autovideosrc name=src0 ! video/x-raw,width=640,height=480 ! videoconvert ! video/x-raw,format=I420 ! vp8enc error-resilient=partitions keyframe-max-dist=10 auto-alt-ref=true cpu-used=5 deadline=1 ! rtpvp8pay ! udpsink host=127.0.0.1 port=5004


## Run webrtc-connector

    $ go build
    $ sudo ./webrtc-connector --session abc

You can control web browser at https://webrtc-sdp-exchanger.appspot.com/example/app/?session=abc


### for debug

    $ ./webrtc-connector --session abc -c /usr/bin/tee -arg "output.txt"
