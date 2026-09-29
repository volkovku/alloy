public final class AlloyThreadE2E {
    private static volatile boolean running = true;

    private static void burnCpu() {
        long value = 1;
        while (running) {
            value = value * 1664525 + 1013904223;
            if (value == 42) {
                System.out.println(value);
            }
        }
    }

    public static void main(String[] args) throws Exception {
        var first = new Thread(AlloyThreadE2E::burnCpu, "http-nio-auto-1-exec-9");
        var second = new Thread(AlloyThreadE2E::burnCpu, "background-worker-42");
        first.start();
        second.start();
        Thread.sleep(120_000);
        running = false;
        first.join();
        second.join();
    }
}
