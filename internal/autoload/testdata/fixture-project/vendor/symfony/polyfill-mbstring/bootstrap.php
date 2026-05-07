<?php

if (!function_exists('mb_strlen')) {
    function mb_strlen($s, $encoding = null) {
        return strlen($s);
    }
}
