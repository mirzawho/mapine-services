module.exports = {
    /**
     * from 1.0.0 to 100
     * @param {string} version 
     * @returns {number}
     */
    formatVersion(version) {
        const ver = version.split('.').map((e) => parseInt(e, 10));

        let r = 0;

        for (let i = 1; i <= ver.length; i++) {
            const j = Math.pow(10, i);
            const v = ver[ver.length - i];
            r += v * j;
        }

        return r;

    }
}