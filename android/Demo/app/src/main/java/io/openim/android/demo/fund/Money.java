package io.openim.android.demo.fund;

/**
 * 金额工具：全部整数分，禁止浮点金额运算。
 * 展示用 "¥" + 分/100 的格式化；输入解析只做字符串整数运算。
 */
public final class Money {

    private Money() {
    }

    /** 分 -> "¥12.34"（整数运算，不用浮点）。 */
    public static String formatCents(long cents) {
        boolean negative = cents < 0;
        long abs = negative ? -cents : cents;
        long yuan = abs / 100;
        long fen = abs % 100;
        StringBuilder sb = new StringBuilder();
        if (negative) {
            sb.append('-');
        }
        sb.append('¥').append(yuan).append('.');
        if (fen < 10) {
            sb.append('0');
        }
        sb.append(fen);
        return sb.toString();
    }

    /**
     * 用户输入的元字符串 -> 分；非法返回 -1。
     * 接受 "10"、"10.5"、"10.05"、"¥10.05"；小数超过 2 位直接拒绝，不做四舍五入。
     */
    public static long parseYuanToCents(String input) {
        if (input == null) {
            return -1;
        }
        String s = input.trim().replace("¥", "").replace(",", "").trim();
        if (s.isEmpty()) {
            return -1;
        }
        String intPart;
        String fracPart = "";
        int dot = s.indexOf('.');
        if (dot >= 0) {
            if (s.indexOf('.', dot + 1) >= 0) {
                return -1;
            }
            intPart = s.substring(0, dot);
            fracPart = s.substring(dot + 1);
        } else {
            intPart = s;
        }
        if (intPart.isEmpty() || fracPart.length() > 2) {
            return -1;
        }
        for (int i = 0; i < intPart.length(); i++) {
            if (!Character.isDigit(intPart.charAt(i))) {
                return -1;
            }
        }
        for (int i = 0; i < fracPart.length(); i++) {
            if (!Character.isDigit(fracPart.charAt(i))) {
                return -1;
            }
        }
        long yuan;
        try {
            yuan = Long.parseLong(intPart);
        } catch (NumberFormatException e) {
            return -1;
        }
        long fen = 0;
        if (fracPart.length() == 1) {
            fen = (fracPart.charAt(0) - '0') * 10L;
        } else if (fracPart.length() == 2) {
            fen = (fracPart.charAt(0) - '0') * 10L + (fracPart.charAt(1) - '0');
        }
        if (yuan > (Long.MAX_VALUE - fen) / 100) {
            return -1;
        }
        return yuan * 100 + fen;
    }
}
